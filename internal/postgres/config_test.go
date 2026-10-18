// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package postgres

import (
	"context"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/exportdata"
)

const secretURL = "postgres://SENTINEL_USER:SENTINEL_PASSWORD@SENTINEL_HOST/fixture?sslmode=verify-full"

func TestConnectionProfile(t *testing.T) {
	for _, raw := range []string{
		secretURL,
		"postgres://user@127.0.0.1:5433/fixture?sslmode=disable",
		"postgres://user@[::1]/fixture?sslmode=disable",
		"postgres://user@/fixture?host=/var/run/postgresql&sslmode=disable",
	} {
		c, err := connectionConfig(raw)
		if err != nil {
			t.Fatalf("accepted profile rejected: %v", err)
		}
		if len(c.Fallbacks) != 0 || c.RuntimeParams["default_transaction_read_only"] != "on" || c.MaxProtocolMessageBodyLen == 0 {
			t.Fatal("unsafe connection defaults")
		}
		if strings.Contains(raw, "verify-full") && (c.TLSConfig == nil || c.TLSConfig.InsecureSkipVerify || c.TLSConfig.ServerName != c.Host) {
			t.Fatal("remote host is not verified")
		}
	}
	for _, raw := range []string{
		"postgres://user:secret@remote/fixture", "postgres://user:secret@remote/fixture?sslmode=disable",
		"postgres://user:secret@remote/fixture?sslmode=verify-ca", "postgres://user:secret@localhost/fixture?sslmode=disable",
		"postgres://user:secret@remote/fixture?sslmode=prefer", "postgres://user@one,two/fixture?sslmode=verify-full",
		"postgres://user@remote/fixture?sslmode=verify-full&sslmode=disable", "postgres://user@remote/fixture?ssl=true",
		"postgres://user@remote/fixture?sslmode=verify-full&host=127.0.0.1", "postgres://user@remote/fixture?sslmode=verify-full&options=-c%20role=postgres",
		"postgres://user@remote/fixture?sslmode=verify-full&service=evil", "postgres://user@remote/fixture?sslmode=verify-full&passfile=/tmp/secret",
		"postgres://user@remote/fixture?sslmode=verify-full&servicefile=/tmp/secret", "postgres://user@remote/fixture?sslmode=verify-full&sslkey=/tmp/secret",
		"postgres://user@remote/fixture?sslmode=verify-full&%73slmode=disable", "postgres://user@remote:0/fixture?sslmode=verify-full",
		"postgres://user@remote:5432,5433/fixture?sslmode=verify-full", "postgres://user@remote/fixture#secret",
		"postgres://user%00other@remote/fixture?sslmode=verify-full", "postgres://user@remote/fi%0axture?sslmode=verify-full",
		"host=remote password=SENTINEL_PASSWORD", "postgres://user@/fixture?host=relative&sslmode=disable",
		"postgres:///fixture?sslmode=verify-full", "postgres://user@remote/?sslmode=verify-full",
	} {
		if _, err := connectionConfig(raw); err == nil {
			t.Fatal("unsafe URL accepted")
		} else if strings.Contains(err.Error(), "SENTINEL") || strings.Contains(err.Error(), "secret") {
			t.Fatal("unsafe diagnostic")
		}
	}
}

func TestInheritedPostgresEnvironment(t *testing.T) {
	for _, key := range []string{"PGHOST", "PGHOSTADDR", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD", "PGPASSFILE", "PGSERVICE", "PGSERVICEFILE", "PGSSLMODE", "PGSSLCERT", "PGSSLKEY", "PGSSLROOTCERT", "PGSSLPASSWORD", "PGOPTIONS", "PGAPPNAME", "PGCONNECT_TIMEOUT", "PGTARGETSESSIONATTRS", "PGTZ", "PGMINPROTOCOLVERSION", "PGMAXPROTOCOLVERSION", "PGCHANNELBINDING", "PGREQUIREAUTH", "PGFUTUREOPTION"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "SENTINEL_INHERITED_SETTING")
			c, err := connectionConfig("postgres://explicit@127.0.0.1/fixture?sslmode=disable")
			if err != nil {
				t.Fatal(err)
			}
			if c.Host != "127.0.0.1" || c.User != "explicit" || c.Database != "fixture" || c.Password != "" || c.Port != 5432 || c.TLSConfig != nil || len(c.RuntimeParams) != 3 || len(c.Fallbacks) != 0 {
				t.Fatal("inherited configuration escaped isolation")
			}
			if os.Getenv(key) != "SENTINEL_INHERITED_SETTING" {
				t.Fatal("caller environment not restored")
			}
		})
	}
}

func TestHomeCredentialAndServiceFilesIgnored(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, item := range []struct{ name, data string }{
		{".pgpass", "127.0.0.1:5432:fixture:explicit:SENTINEL_HOME_PASSWORD\n"},
		{".pg_service.conf", "[evil]\nhost=SENTINEL_HOME_HOST\npassword=SENTINEL_HOME_PASSWORD\nsslmode=disable\n"},
	} {
		if err := os.WriteFile(filepath.Join(home, item.name), []byte(item.data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PGSERVICE", "evil")
	c, err := connectionConfig("postgres://explicit@127.0.0.1/fixture?sslmode=disable")
	if err != nil || c.Password != "" || c.Host != "127.0.0.1" {
		t.Fatal("home files affected explicit credentials")
	}
}

func TestHomeCertificateFilesIgnored(t *testing.T) {
	if os.Getenv("FLEETDIFF_TEST_HOME_CERT") == "child" {
		if _, err := neutralConfig(); err != nil {
			t.Fatal(err)
		}
		return
	}
	for _, fifo := range []bool{false, true} {
		home := t.TempDir()
		dir := filepath.Join(home, ".postgresql")
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"root.crt", "postgresql.crt", "postgresql.key"} {
			path := filepath.Join(dir, name)
			var err error
			if fifo {
				err = syscall.Mkfifo(path, 0600)
			} else {
				err = os.WriteFile(path, []byte("SENTINEL_INVALID_HOME_CERTIFICATE"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cmd := osexec.CommandContext(ctx, os.Args[0], "-test.run=^TestHomeCertificateFilesIgnored$")
		cmd.Env = append(os.Environ(), "HOME="+home, "FLEETDIFF_TEST_HOME_CERT=child")
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("home certificates affected isolated parser: %v %s", err, out)
		}
	}
}

func TestCredentialChecksFollowReadDescriptor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials")
	if err := os.WriteFile(path, []byte(secretURL), 0644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte(secretURL), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredentialFile(f); err == nil {
		t.Fatal("permissions checked on replacement path instead of read descriptor")
	}
}

func TestCredentialFiles(t *testing.T) {
	t.Setenv(credentialEnv, "")
	if err := os.Unsetenv(credentialEnv); err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "credentials")
	if err := os.WriteFile(path, []byte(secretURL+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := credentials(path); err != nil || got != secretURL {
		t.Fatal("owner-only credential file rejected")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := credentials(link); err == nil {
		t.Fatal("symlink credentials accepted")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := credentials(path); err == nil {
		t.Fatal("public credential file accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(credentialEnv, secretURL)
	if _, err := credentials(path); err == nil {
		t.Fatal("ambiguous credential sources accepted")
	}
}

func TestExportErrorPrivacy(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(credentialEnv, "postgres://SENTINEL_USER:SENTINEL_PASSWORD@127.0.0.1:1/fixture?sslmode=disable")
	o := exportdata.Options{Directory: filepath.Join(root, "SENTINEL_PATH"), Start: time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC), Format: "csv", UsageDetails: true}
	_, err = Export(context.Background(), o)
	if err == nil || strings.Contains(err.Error(), "SENTINEL") || strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), root) {
		t.Fatal("database diagnostics were not safe")
	}
	if _, err := os.Stat(o.Directory); !os.IsNotExist(err) {
		t.Fatal("failed export left an output directory")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = Export(ctx, o); !strings.Contains(err.Error(), "interrupted") {
		t.Fatal("cancellation not reported")
	}
}

func FuzzConnectionProfile(f *testing.F) {
	for _, raw := range []string{secretURL, "postgres://u@127.0.0.1/d?sslmode=disable", "postgres://u@remote/d?sslmode=verify-full&service=evil", "postgres://u@remote/d?sslmode=verify-full&sslmode=disable"} {
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 65536 || strings.Contains(raw, "sslrootcert") || strings.Contains(raw, "%") {
			return
		}
		c, err := connectionConfig(raw)
		if err != nil {
			if strings.Contains(err.Error(), "SENTINEL") || strings.Contains(err.Error(), raw) && len(raw) > 16 {
				t.Fatal("connection input escaped into an error")
			}
			return
		}
		if len(c.Fallbacks) != 0 || c.RuntimeParams["default_transaction_read_only"] != "on" {
			t.Fatal("unsafe config")
		}
		if c.TLSConfig != nil && (c.TLSConfig.InsecureSkipVerify || c.TLSConfig.ServerName != c.Host) {
			t.Fatal("host verification lost")
		}
	})
}
