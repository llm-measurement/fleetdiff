// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This is a source-level capability guard, not a sandbox or a reachability
// analysis. Check the current GOOS/GOARCH and tags supplied through GOFLAGS,
// including dependency code and uncalled functions, but not tests or unrelated
// examples. CI must run it for each supported build configuration; go test's
// command-line-only tags are not inherited by go list. Standard-library code is
// trusted: scanning net itself would reject the permitted SplitHostPort helper.
// scripts/restricted-smoke.sh also compares built Linux CLI outputs with Docker
// bridge networking versus --network none in CI. Assembly, reflection and
// dynamic loading still require review; neither test proves absence of all I/O.
// Explicit database export permits only the reviewed adapter/pgconn dial and
// resolver APIs below. All packages still receive the HTTP/process/cgo checks;
// offline packages cannot import the adapter or its driver runtime at all.
func TestProductionHasNoNetwork(t *testing.T) {
	packages := listNetworkPackages(t, "../..")
	for _, finding := range networkFindings(t, packages) {
		t.Error(finding)
	}
}

type networkPackage struct {
	ImportPath string
	Dir        string
	Export     string
	Standard   bool
	GoFiles    []string
	CgoFiles   []string
	Imports    []string
	Module     *networkModule
}

type networkModule struct {
	Path, Version string
	Replace       *networkModule
}

const databaseAdapter = "github.com/llm-measurement/fleetdiff/internal/postgres"
const databaseMain = "github.com/llm-measurement/fleetdiff/cmd/fleetdiff"

// Exact runtime packages, not a module-prefix exemption. New driver packages
// and upgrades require review; shared x/text continues to be scanned normally.
func databaseRuntimeModule(path string) (string, string) {
	switch path {
	case "github.com/jackc/pgpassfile":
		return path, "v1.0.0"
	case "github.com/jackc/pgservicefile":
		return path, "v0.0.0-20240606120523-5a60cdf6a761"
	case "github.com/jackc/pgx/v5/pgconn",
		"github.com/jackc/pgx/v5/pgconn/ctxwatch",
		"github.com/jackc/pgx/v5/pgconn/internal/bgreader",
		"github.com/jackc/pgx/v5/pgproto3",
		"github.com/jackc/pgx/v5/pgtype",
		"github.com/jackc/pgx/v5/internal/pgio",
		"github.com/jackc/pgx/v5/internal/pgdatetime",
		"github.com/jackc/pgx/v5/internal/iobufpool":
		return "github.com/jackc/pgx/v5", "v5.11.0"
	}
	return "", ""
}

func databaseBoundaryFindings(packages []networkPackage) []string {
	var findings []string
	for _, p := range packages {
		module, version := databaseRuntimeModule(p.ImportPath)
		if strings.HasPrefix(p.ImportPath, "github.com/jackc/") && module == "" {
			findings = append(findings, p.ImportPath+": unreviewed database runtime package")
		}
		if module != "" && (p.Module == nil || p.Module.Path != module || p.Module.Version != version || p.Module.Replace != nil) {
			findings = append(findings, p.ImportPath+": database runtime differs from approved module pin")
		}
		for _, imported := range p.Imports {
			if imported == databaseAdapter && p.ImportPath != databaseMain {
				findings = append(findings, p.ImportPath+": offline code imports database adapter")
			}
			if strings.HasPrefix(imported, "github.com/jackc/") && p.ImportPath != databaseAdapter && module == "" {
				findings = append(findings, p.ImportPath+": offline code imports database runtime")
			}
		}
	}
	return findings
}

func databaseNetworkFunction(owner, path, name string) bool {
	return (owner == databaseAdapter || owner == "github.com/jackc/pgx/v5/pgconn") &&
		path == "net" && (name == "DialContext" || name == "LookupHost")
}

func listNetworkPackages(t *testing.T, dir string) []networkPackage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// No -test: test-only clients must not contaminate the production graph.
	cmd := exec.CommandContext(ctx, "go", "list", "-deps", "-export", "-json", "./cmd/...", "./internal/...")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("load production graph: %v\n%s", err, &stderr)
	}
	var packages []networkPackage
	decoder := json.NewDecoder(bytes.NewReader(output))
	for {
		var p networkPackage
		if err := decoder.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		packages = append(packages, p)
	}
	if len(packages) == 0 {
		t.Fatal("empty production graph")
	}
	return packages
}

func networkFindings(t *testing.T, packages []networkPackage) []string {
	t.Helper()
	fset := token.NewFileSet()
	exports := make(map[string]string)
	for _, p := range packages {
		exports[p.ImportPath] = p.Export
	}
	imp := importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		return os.Open(exports[path])
	})
	findings := databaseBoundaryFindings(packages)
	for _, p := range packages {
		if networkImport(p.ImportPath) {
			findings = append(findings, p.ImportPath+": forbidden dependency in production graph")
		}
		if p.Standard {
			continue
		}
		// Cgo can open sockets without a Go API reference; fail closed rather
		// than silently claiming to have checked its C implementation.
		if len(p.CgoFiles) != 0 {
			findings = append(findings, p.ImportPath+": cgo requires an explicit offline review")
			continue
		}
		var files []*ast.File
		for _, name := range p.GoFiles {
			f, err := parser.ParseFile(fset, filepath.Join(p.Dir, name), nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, f)
			for _, spec := range f.Imports {
				path, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				if networkImport(path) {
					findings = append(findings, fmt.Sprintf("%s: %s: forbidden import %s", p.ImportPath, fset.Position(spec.Pos()), path))
				}
			}
		}
		info := &types.Info{Uses: make(map[*ast.Ident]types.Object), Types: make(map[ast.Expr]types.TypeAndValue)}
		config := types.Config{Importer: imp, Sizes: types.SizesFor("gc", runtime.GOARCH)}
		if _, err := config.Check(p.ImportPath, fset, files, info); err != nil {
			t.Fatalf("type-check %s for network guard: %v", p.ImportPath, err)
		}
		filesystemCalls := reviewedFilesystemSyscalls(p.ImportPath, files, fset, info)
		// Resolve symbols, not selector spelling. This catches import aliases,
		// dot imports, method expressions, and saved function values while
		// leaving shadowed names and unrelated Dial/Listen methods alone.
		for id, obj := range info.Uses {
			if obj.Pkg() == nil {
				continue
			}
			if _, ok := obj.(*types.Func); !ok {
				continue
			}
			if networkFunction(obj.Pkg().Path(), obj.Name()) && !databaseNetworkFunction(p.ImportPath, obj.Pkg().Path(), obj.Name()) && !filesystemCalls[id] {
				findings = append(findings, fmt.Sprintf("%s: %s: forbidden network/process API %s.%s", p.ImportPath, fset.Position(id.Pos()), obj.Pkg().Path(), obj.Name()))
			}
		}
	}
	slices.Sort(findings)
	return findings
}

// Permit only direct, constant-operation calls in the two audited filesystem
// wrappers. Saved syscall functions, variable operation/flag values, other
// files, and all socket/process operations remain forbidden.
func reviewedFilesystemSyscalls(owner string, files []*ast.File, fset *token.FileSet, info *types.Info) map[*ast.Ident]bool {
	allowed := make(map[*ast.Ident]bool)
	if owner != databaseAdapter {
		return allowed
	}
	for _, file := range files {
		filename := filepath.Base(fset.Position(file.Pos()).Filename)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok || len(call.Args) != 7 || call.Ellipsis.IsValid() {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				obj, ok := info.Uses[selector.Sel].(*types.Func)
				if !ok || obj.Pkg() == nil || obj.Pkg().Path() != "syscall" || obj.Name() != "Syscall6" {
					return true
				}
				is := func(index int, want int64) bool {
					value := info.Types[call.Args[index]].Value
					if value == nil || value.Kind() != constant.Int {
						return false
					}
					got, ok := constant.Int64Val(value)
					return ok && got == want
				}
				var rename, unlink, noReplace, removeDir int64
				switch filename {
				case "rename_darwin.go":
					rename, unlink, noReplace, removeDir = 488, 472, 4, 0x80
				case "rename_linux.go":
					noReplace, removeDir = 1, 0x200
					switch runtime.GOARCH {
					case "amd64":
						rename, unlink = 316, 263
					case "arm64":
						rename, unlink = 276, 35
					default:
						return true
					}
				default:
					return true
				}
				switch fn.Name.Name {
				case "renameDirectoryNoReplace":
					allowed[selector.Sel] = is(0, rename) && is(5, noReplace) && is(6, 0)
				case "removeEmptyDirectoryAt":
					allowed[selector.Sel] = is(0, unlink) && is(3, removeDir) && is(4, 0) && is(5, 0) && is(6, 0)
				}
				return true
			})
		}
	}
	return allowed
}

func networkImport(path string) bool {
	// Keep parsing/value packages (net/url, net/netip, net/mail) available.
	return path == "net/http" || strings.HasPrefix(path, "net/http/") ||
		path == "net/rpc" || strings.HasPrefix(path, "net/rpc/") || path == "net/smtp" || path == "log/syslog" || path == "os/exec"
}

func networkFunction(path, name string) bool {
	switch path {
	case "os":
		return name == "StartProcess"
	case "net", "crypto/tls", "net/textproto":
		for _, prefix := range []string{"Dial", "Listen", "Accept", "Lookup", "Resolve"} {
			if strings.HasPrefix(name, prefix) && name != "ResolveUnixAddr" {
				return true
			}
		}
		return name == "FileConn" || name == "FileListener" || name == "FilePacketConn"
	case "syscall", "golang.org/x/sys/unix", "golang.org/x/sys/windows":
		if name == "Exec" || name == "ForkExec" || name == "StartProcess" || strings.HasPrefix(name, "CreateProcess") {
			return true
		}
		// Raw syscalls can hide socket operations behind numeric constants.
		for _, prefix := range []string{"Socket", "WSA", "Accept", "Bind", "Connect", "Listen", "Sendto", "Sendmsg", "Recvfrom", "Recvmsg", "Getsock", "Setsock", "Shutdown", "Syscall", "RawSyscall"} {
			if strings.HasPrefix(name, prefix) {
				return true
			}
		}
	}
	return false
}

func TestNetworkGuardFixtures(t *testing.T) {
	fixtures := []struct {
		name   string
		source string
		want   string
	}{
		{"http", `import "net/http"; var _ = http.MethodGet`, "forbidden import net/http"},
		{"http_alias", `import client "net/http"; var _ = client.Get`, "forbidden import net/http"},
		{"http_blank", `import _ "net/http"`, "forbidden import net/http"},
		{"http_dot", `import . "net/http"; var _ = MethodGet`, "forbidden import net/http"},
		{"http_subpackage", `import _ "net/http/pprof"`, "forbidden import net/http/pprof"},
		{"syslog", `import "log/syslog"; var _ = syslog.New`, "forbidden import log/syslog"},
		{"smtp", `import "net/smtp"; var _ = smtp.SendMail`, "forbidden import net/smtp"},
		{"rpc", `import "net/rpc"; var _ = rpc.Dial`, "forbidden import net/rpc"},
		{"exec", `import "os/exec"; var _ = exec.Command`, "forbidden import os/exec"},
		{"exec_alias", `import process "os/exec"; var _ = process.CommandContext`, "forbidden import os/exec"},
		{"exec_blank", `import _ "os/exec"`, "forbidden import os/exec"},
		{"exec_dot", `import . "os/exec"; var _ = Command`, "forbidden import os/exec"},
		{"start_process", `import "os"; var _ = os.StartProcess`, "os.StartProcess"},
		{"start_process_alias", `import process "os"; var _ = process.StartProcess`, "os.StartProcess"},
		{"start_process_dot", `import . "os"; var _ = StartProcess`, "os.StartProcess"},
		{"syscall_exec", `import "syscall"; var _ = syscall.Exec`, "syscall.Exec"},
		{"syscall_fork_exec", `import "syscall"; var _ = syscall.ForkExec`, "syscall.ForkExec"},
		{"dial", `import "net"; func unused() { _, _ = net.Dial("tcp", "localhost:1") }`, "net.Dial"},
		{"dial_alias", `import socket "net"; var saved = socket.DialTimeout`, "net.DialTimeout"},
		{"dial_dot", `import . "net"; var saved = Dial`, "net.Dial"},
		{"dialer", `import "net"; var d net.Dialer; var saved = d.DialContext`, "net.DialContext"},
		{"dialer_alias", `import "net"; type local = net.Dialer; var saved = (*local).Dial`, "net.Dial"},
		{"dial_tcp", `import "net"; var _ = net.DialTCP`, "net.DialTCP"},
		{"dial_udp", `import "net"; var _ = net.DialUDP`, "net.DialUDP"},
		{"dial_unix", `import "net"; var _ = net.DialUnix`, "net.DialUnix"},
		{"listen", `import "net"; var _ = net.Listen`, "net.Listen"},
		{"listen_packet", `import "net"; var _ = net.ListenPacket`, "net.ListenPacket"},
		{"listen_udp", `import "net"; var _ = net.ListenUDP`, "net.ListenUDP"},
		{"listen_config", `import "net"; var _ = (*net.ListenConfig).Listen`, "net.Listen"},
		{"accept", `import "net"; var _ = (*net.TCPListener).AcceptTCP`, "net.AcceptTCP"},
		{"dns", `import "net"; var _ = net.LookupHost`, "net.LookupHost"},
		{"resolver", `import "net"; var _ = net.DefaultResolver.LookupIPAddr`, "net.LookupIPAddr"},
		{"resolve", `import "net"; var _ = net.ResolveTCPAddr`, "net.ResolveTCPAddr"},
		{"file_conn", `import "net"; var _ = net.FileConn`, "net.FileConn"},
		{"file_listener", `import "net"; var _ = net.FileListener`, "net.FileListener"},
		{"file_packet", `import "net"; var _ = net.FilePacketConn`, "net.FilePacketConn"},
		{"listener_interface", `import "net"; var _ = net.Listener.Accept`, "net.Accept"},
		{"tls_dial", `import "crypto/tls"; var _ = tls.Dial`, "crypto/tls.Dial"},
		{"tls_listen", `import "crypto/tls"; var _ = tls.Listen`, "crypto/tls.Listen"},
		{"tls_dialer", `import "crypto/tls"; var _ = (*tls.Dialer).DialContext`, "crypto/tls.DialContext"},
		{"textproto", `import "net/textproto"; var _ = textproto.Dial`, "net/textproto.Dial"},
		{"socket", `import s "syscall"; var _ = s.Socket`, "syscall.Socket"},
		{"connect", `import "syscall"; var _ = syscall.Connect`, "syscall.Connect"},
		{"bind", `import "syscall"; var _ = syscall.Bind`, "syscall.Bind"},
		{"raw_syscall", `import "syscall"; var _ = syscall.RawSyscall`, "syscall.RawSyscall"},
		{"syscall", `import "syscall"; var _ = syscall.Syscall`, "syscall.Syscall"},
		{"safe_url", `import "net/url"; var _ = url.Parse`, ""},
		{"safe_split", `import n "net"; var _ = n.SplitHostPort; var _ = n.JoinHostPort`, ""},
		{"safe_dot", `import . "net"; var _ = SplitHostPort`, ""},
		{"safe_ip", `import "net"; import "net/netip"; var _ = net.ParseIP; var _ = netip.ParseAddr`, ""},
		{"safe_mail", `import "net/mail"; var _ = mail.ParseAddress`, ""},
		{"safe_textproto", `import "net/textproto"; var _ = textproto.CanonicalMIMEHeaderKey`, ""},
		{"safe_syscall", `import "syscall"; var _ = syscall.O_NONBLOCK; var _ = syscall.Getpid`, ""},
		{"safe_os", `import "os"; var _ = os.Open; var _ = os.Getpid`, ""},
		{"safe_process_shadow", `import "os"; var _ = os.Open; func f() { os := struct { StartProcess func() }{}; os.StartProcess() }`, ""},
		{"safe_pipe", `import "net"; var _ = net.Pipe`, ""},
		{"safe_shadow", `import "net"; var _ = net.SplitHostPort; func f() { net := struct { Dial func() }{}; net.Dial() }`, ""},
		{"safe_method", `type local struct{}; func (local) Dial() {}; var _ = local{}.Dial`, ""},
		{"safe_text", "// net.Dial and import net/http are only text.\nconst text = `net.Listen net/http syscall.Socket`", ""},
	}
	root := t.TempDir()
	writeNetworkFixture(t, root, "go.mod", "module guardfixture\n\ngo 1.26.0\n\nrequire example.com/client v0.0.0\nreplace example.com/client => ./dependency\n")
	for _, fixture := range fixtures {
		writeNetworkFixture(t, root, "internal/"+fixture.name+"/source.go", "package fixture\n"+fixture.source+"\n")
	}
	writeNetworkFixture(t, root, "cmd/tool/main.go", "package main\nimport _ \"example.com/client\"\nfunc main() {}\n")
	writeNetworkFixture(t, root, "dependency/go.mod", "module example.com/client\n\ngo 1.26.0\n")
	writeNetworkFixture(t, root, "dependency/client.go", "package client\nimport _ \"example.com/client/deeper\"\n")
	writeNetworkFixture(t, root, "dependency/deeper/client.go", "package deeper\nimport \"net\"\nvar saved = net.Dial\n")
	writeNetworkFixture(t, root, "dependency/deeper/http.go", "package deeper\nimport _ \"net/http\"\n")
	writeNetworkFixture(t, root, "dependency/deeper/process.go", "package deeper\nimport \"os\"\nimport _ \"os/exec\"\nvar _ = os.StartProcess\n")
	writeNetworkFixture(t, root, "examples/live/main.go", "package main\nimport _ \"net/http\"\nfunc main() {}\n")
	writeNetworkFixture(t, root, "internal/safe_url/client_test.go", "package fixture\nimport _ \"net/http\"\n")
	packages := listNetworkPackages(t, root)
	findings := networkFindings(t, packages)
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			var got []string
			for _, finding := range findings {
				if strings.HasPrefix(finding, "guardfixture/internal/"+fixture.name+":") {
					got = append(got, finding)
				}
			}
			if fixture.want == "" && len(got) != 0 || fixture.want != "" && !strings.Contains(strings.Join(got, "\n"), fixture.want) {
				t.Fatalf("want %q; findings: %v", fixture.want, got)
			}
		})
	}
	for _, want := range []string{"net.Dial", "forbidden import net/http", "forbidden import os/exec", "os.StartProcess"} {
		found := false
		for _, finding := range findings {
			if strings.HasPrefix(finding, "example.com/client/deeper:") && strings.Contains(finding, want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("missed %s in transitive dependency", want)
		}
	}
	for _, p := range packages {
		if strings.Contains(p.ImportPath, "examples/live") || strings.HasSuffix(p.ImportPath, ".test") || len(p.GoFiles) == 0 && !p.Standard {
			t.Fatalf("unexpected package in production graph: %+v", p)
		}
		for _, name := range p.GoFiles {
			if strings.HasSuffix(name, "_test.go") {
				t.Fatalf("test file included in production graph: %s", name)
			}
		}
	}
}

func TestNetworkGuardProductionGraph(t *testing.T) {
	root := t.TempDir()
	writeNetworkFixture(t, root, "go.mod", "module guardfixture\n\ngo 1.26.0\n")
	writeNetworkFixture(t, root, "cmd/tool/main.go", "package main\nfunc main() {}\n")
	writeNetworkFixture(t, root, "internal/helper/helper.go", "package helper\nimport \"net\"\nvar _ = net.SplitHostPort\n")
	writeNetworkFixture(t, root, "internal/helper/helper_test.go", "package helper\nimport _ \"net/http\"\nimport _ \"os/exec\"\n")
	writeNetworkFixture(t, root, "examples/live/main.go", "package main\nimport _ \"net/http\"\nfunc main() {}\n")
	if got := networkFindings(t, listNetworkPackages(t, root)); len(got) != 0 {
		t.Fatalf("test-only/example networking contaminated production graph: %v", got)
	}
	// expvar brings in net/http through the standard library, whose bodies
	// are not type-checked by this guard. The dependency graph must catch it.
	writeNetworkFixture(t, root, "cmd/tool/main.go", "package main\nimport _ \"expvar\"\nfunc main() {}\n")
	got := networkFindings(t, listNetworkPackages(t, root))
	if !slices.Contains(got, "net/http: forbidden dependency in production graph") {
		t.Fatalf("missed indirect standard-library HTTP dependency: %v", got)
	}
}

func TestNetworkGuardLowLevelPolicy(t *testing.T) {
	for _, path := range []string{"syscall", "golang.org/x/sys/unix", "golang.org/x/sys/windows"} {
		for _, name := range []string{"Socket", "Socketpair", "Accept4", "Bind", "Connect", "ConnectEx", "Listen", "Sendto", "SendmsgN", "Recvfrom", "Recvmsg", "WSASocket", "Syscall6", "RawSyscall6", "Exec", "ForkExec", "StartProcess", "CreateProcessAsUser"} {
			if !networkFunction(path, name) {
				t.Errorf("missed %s.%s", path, name)
			}
		}
		for _, name := range []string{"Open", "Close", "Read", "Fstat", "Getpid"} {
			if networkFunction(path, name) {
				t.Errorf("incorrectly rejected %s.%s", path, name)
			}
		}
	}
	got := networkFindings(t, []networkPackage{{ImportPath: "fixture/cgo", CgoFiles: []string{"socket.go"}}})
	if len(got) != 1 || !strings.Contains(got[0], "cgo requires an explicit offline review") {
		t.Fatalf("cgo was not rejected: %v", got)
	}
}

func TestNetworkGuardDatabaseBoundary(t *testing.T) {
	pgconn := "github.com/jackc/pgx/v5/pgconn"
	module, version := databaseRuntimeModule(pgconn)
	approved := []networkPackage{
		{ImportPath: databaseMain, Imports: []string{databaseAdapter}},
		{ImportPath: databaseAdapter, Imports: []string{pgconn}},
		{ImportPath: pgconn, Module: &networkModule{Path: module, Version: version}},
	}
	if got := databaseBoundaryFindings(approved); len(got) != 0 {
		t.Fatalf("reviewed database boundary rejected: %v", got)
	}
	for _, p := range []networkPackage{
		{ImportPath: "github.com/llm-measurement/fleetdiff/internal/cli", Imports: []string{databaseAdapter}},
		{ImportPath: "github.com/llm-measurement/fleetdiff/internal/spend", Imports: []string{pgconn}},
		{ImportPath: databaseMain, Imports: []string{pgconn}},
		{ImportPath: "example.com/transitive", Imports: []string{pgconn}},
		{ImportPath: "github.com/jackc/pgx/v5/unreviewed"},
		{ImportPath: pgconn, Module: &networkModule{Path: module, Version: "v5.99.0"}},
		{ImportPath: pgconn, Module: &networkModule{Path: module, Version: version, Replace: &networkModule{Path: "./local"}}},
	} {
		if got := databaseBoundaryFindings([]networkPackage{p}); len(got) == 0 {
			t.Errorf("database boundary escape accepted: %+v", p)
		}
	}
	for _, owner := range []string{databaseAdapter, pgconn, databaseMain, "github.com/llm-measurement/fleetdiff/internal/cli"} {
		for _, api := range [][2]string{{"net", "DialContext"}, {"net", "LookupHost"}, {"net", "Listen"}, {"os", "StartProcess"}, {"syscall", "Socket"}, {"net/http", "Get"}} {
			want := (owner == databaseAdapter || owner == pgconn) && api[0] == "net" && (api[1] == "DialContext" || api[1] == "LookupHost")
			if got := databaseNetworkFunction(owner, api[0], api[1]); got != want {
				t.Errorf("database API exception %s %v = %v, want %v", owner, api, got, want)
			}
		}
	}
}

func TestNetworkGuardFilesystemSyscallExceptions(t *testing.T) {
	linuxRename, linuxUnlink := 316, 263
	if runtime.GOARCH == "arm64" {
		linuxRename, linuxUnlink = 276, 35
	}
	for _, tc := range []struct {
		name, file, function, declarations, body string
		want                                     bool
	}{
		{"darwin_rename", "rename_darwin.go", "renameDirectoryNoReplace", "", "syscall.Syscall6(488, 0, 0, 0, 0, 4, 0)", true},
		{"darwin_rmdir", "rename_darwin.go", "removeEmptyDirectoryAt", "", "syscall.Syscall6(472, 0, 0, 128, 0, 0, 0)", true},
		{"linux_rename", "rename_linux.go", "renameDirectoryNoReplace", fmt.Sprintf("const operation = %d", linuxRename), "syscall.Syscall6(operation, 0, 0, 0, 0, 1, 0)", true},
		{"linux_rmdir", "rename_linux.go", "removeEmptyDirectoryAt", "", fmt.Sprintf("syscall.Syscall6(%d, 0, 0, 512, 0, 0, 0)", linuxUnlink), true},
		{"wrong_file", "files.go", "renameDirectoryNoReplace", "", "syscall.Syscall6(488, 0, 0, 0, 0, 4, 0)", false},
		{"wrong_function", "rename_darwin.go", "other", "", "syscall.Syscall6(488, 0, 0, 0, 0, 4, 0)", false},
		{"variable_operation", "rename_darwin.go", "renameDirectoryNoReplace", "var operation uintptr = 488", "syscall.Syscall6(operation, 0, 0, 0, 0, 4, 0)", false},
		{"variable_flags", "rename_darwin.go", "renameDirectoryNoReplace", "var flags uintptr = 4", "syscall.Syscall6(488, 0, 0, 0, 0, flags, 0)", false},
		{"overwriting_rename", "rename_darwin.go", "renameDirectoryNoReplace", "", "syscall.Syscall6(488, 0, 0, 0, 0, 0, 0)", false},
		{"unlink_file", "rename_darwin.go", "removeEmptyDirectoryAt", "", "syscall.Syscall6(472, 0, 0, 0, 0, 0, 0)", false},
		{"saved_function", "rename_darwin.go", "renameDirectoryNoReplace", "var saved = syscall.Syscall6", "saved(488, 0, 0, 0, 0, 4, 0)", false},
		{"raw_syscall", "rename_darwin.go", "renameDirectoryNoReplace", "", "syscall.RawSyscall6(488, 0, 0, 0, 0, 4, 0)", false},
		{"socket", "rename_darwin.go", "renameDirectoryNoReplace", "", "syscall.Syscall6(syscall.SYS_SOCKET, 0, 0, 0, 0, 4, 0)", false},
		{"process", "rename_linux.go", "renameDirectoryNoReplace", "", "syscall.Syscall6(syscall.SYS_EXECVE, 0, 0, 0, 0, 1, 0)", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			source := "package postgres\nimport \"syscall\"\n" + tc.declarations + "\nfunc " + tc.function + "() { " + tc.body + " }"
			file, err := parser.ParseFile(fset, tc.file, source, 0)
			if err != nil {
				t.Fatal(err)
			}
			info := &types.Info{Uses: make(map[*ast.Ident]types.Object), Types: make(map[ast.Expr]types.TypeAndValue)}
			config := types.Config{Importer: importer.Default()}
			if _, err := config.Check(databaseAdapter, fset, []*ast.File{file}, info); err != nil {
				t.Fatal(err)
			}
			for _, owner := range []string{databaseAdapter, "offline/package"} {
				allowed := reviewedFilesystemSyscalls(owner, []*ast.File{file}, fset, info)
				permitted, denied := 0, 0
				for id, obj := range info.Uses {
					if obj.Pkg() != nil && obj.Pkg().Path() == "syscall" && networkFunction(obj.Pkg().Path(), obj.Name()) {
						if allowed[id] {
							permitted++
						} else {
							denied++
						}
					}
				}
				want := tc.want && owner == databaseAdapter
				if want && (permitted != 1 || denied != 0) || !want && (permitted != 0 || denied == 0) {
					t.Fatalf("owner=%s permitted=%d denied=%d want exception=%t", owner, permitted, denied, want)
				}
			}
		})
	}
}

func writeNetworkFixture(t *testing.T, root, name, source string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
}
