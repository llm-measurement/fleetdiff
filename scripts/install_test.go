// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package scripts

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstall(t *testing.T) {
	for _, tc := range []struct {
		name, os, arch, target, failure string
	}{
		{"linux amd64", "Linux", "x86_64", "linux_amd64", ""},
		{"linux arm64", "Linux", "aarch64", "linux_arm64", ""},
		{"mac intel", "Darwin", "x86_64", "darwin_amd64", ""},
		{"mac silicon", "Darwin", "arm64", "darwin_arm64", ""},
		{"amd64 alias", "Linux", "amd64", "linux_amd64", ""},
		{"unsupported os", "FreeBSD", "amd64", "", "platform"},
		{"unsupported arch", "Linux", "riscv64", "", "platform"},
		{"download failure", "Darwin", "arm64", "darwin_arm64", "download"},
		{"archive attestation failure", "Darwin", "arm64", "darwin_arm64", "archive"},
		{"checksum attestation failure", "Darwin", "arm64", "darwin_arm64", "manifest"},
		{"checksum mismatch", "Darwin", "arm64", "darwin_arm64", "checksum"},
		{"missing archive", "Darwin", "arm64", "darwin_arm64", "missing"},
		{"existing directory", "Darwin", "arm64", "darwin_arm64", "existing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, env := installEnvironment(t, tc.os, tc.arch, tc.target, tc.failure)
			script, err := filepath.Abs("install.sh")
			if err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(root, "install with spaces")
			if tc.failure == "existing" {
				if err := os.Mkdir(destination, 0700); err != nil {
					t.Fatal(err)
				}
				writeTestFile(t, filepath.Join(destination, "keep"), "unchanged", 0600)
			}
			cmd := exec.Command("sh", script, "v0.3.0", destination)
			cmd.Env = env
			output, err := cmd.CombinedOutput()
			if tc.failure == "" {
				if err != nil {
					t.Fatalf("installer: %v\n%s", err, output)
				}
				if _, err := os.Stat(filepath.Join(destination, "fleetdiff")); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(output), "Verified "+strings.ReplaceAll(tc.target, "_", "/")) {
					t.Fatalf("wrong platform: %s", output)
				}
				log, err := os.ReadFile(filepath.Join(root, "attestations"))
				if err != nil || strings.Count(string(log), "attestation verify") != 2 {
					t.Fatalf("both assets must be attested: %s (%v)", log, err)
				}
			} else {
				if err == nil {
					t.Fatalf("failure accepted: %s", output)
				}
				if _, err := os.Stat(filepath.Join(destination, "fleetdiff")); !os.IsNotExist(err) {
					t.Fatalf("extracted before checks passed: %v", err)
				}
				if tc.failure == "existing" {
					data, err := os.ReadFile(filepath.Join(destination, "keep"))
					if err != nil || string(data) != "unchanged" {
						t.Fatal("modified existing directory")
					}
				}
			}
		})
	}
}

func TestDocumentedInstall(t *testing.T) {
	doc, err := os.ReadFile("../docs/OPERATIONS.md")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(doc), "```sh\n")
	if !ok {
		t.Fatal("missing install example")
	}
	block, _, ok := strings.Cut(rest, "\n```")
	if !ok {
		t.Fatal("unterminated install example")
	}
	for _, failure := range []string{"", "archive", "manifest", "checksum", "missing"} {
		t.Run("failure="+failure, func(t *testing.T) {
			root, env := installEnvironment(t, "Darwin", "arm64", "darwin_arm64", failure)
			destination := filepath.Join(root, "documented-install")
			if err := os.Mkdir(destination, 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "-c", block)
			cmd.Dir, cmd.Env = destination, env
			output, err := cmd.CombinedOutput()
			if (err != nil) != (failure != "") {
				t.Fatalf("install example: %v\n%s", err, output)
			}
			_, statErr := os.Stat(filepath.Join(destination, "fleetdiff"))
			if failure != "" && !os.IsNotExist(statErr) {
				t.Fatalf("documentation extracts on failed checks: %v", statErr)
			}
		})
	}
}

func TestInstallOptions(t *testing.T) {
	script, err := filepath.Abs("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--help"}, {"dev"}, {""}, {"v0.3.0/other"}, {"v0.3.0", ""}, {"v0.3.0", "new", "extra"}} {
		t.Run(fmt.Sprint(args), func(t *testing.T) {
			cmd := exec.Command("sh", append([]string{script}, args...)...)
			cmd.Dir = t.TempDir()
			output, err := cmd.CombinedOutput()
			if args[0] == "--help" {
				if err != nil || !strings.Contains(string(output), "Usage:") {
					t.Fatalf("help: %v\n%s", err, output)
				}
			} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 {
				t.Fatalf("expected usage error: %v\n%s", err, output)
			}
		})
	}
}

func installEnvironment(t *testing.T, system, arch, target, failure string) (string, []string) {
	t.Helper()
	for _, command := range []string{"shasum", "tar"} {
		if _, err := exec.LookPath(command); err != nil {
			t.Skipf("installer test requires %s", command)
		}
	}
	root := t.TempDir()
	bin, assets := filepath.Join(root, "bin"), filepath.Join(root, "assets")
	for _, dir := range []string{bin, assets} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	name := "fleetdiff_v0.3.0_" + target + ".tar.gz"
	var archive strings.Builder
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	content := "#!/bin/sh\nprintf '%s\\n' 'synthetic installer test binary'\n"
	if err := tw.WriteHeader(&tar.Header{Name: "fleetdiff", Mode: 0755, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	data := archive.String()
	checksum := fmt.Sprintf("%x  %s\n%x  other-platform.tar.gz\n", sha256.Sum256([]byte(data)), name, sha256.Sum256(nil))
	if failure == "checksum" {
		data += "tampered"
	}
	writeTestFile(t, filepath.Join(assets, name), data, 0600)
	writeTestFile(t, filepath.Join(assets, "SHA256SUMS"), checksum, 0600)
	writeTestFile(t, filepath.Join(assets, "provenance.jsonl"), "mock provenance\n", 0600)
	writeTestFile(t, filepath.Join(bin, "uname"), "#!/bin/sh\ncase \"$1\" in -s) echo \"$TEST_SYSTEM\" ;; -m) echo \"$TEST_ARCH\" ;; *) exit 1 ;; esac\n", 0700)
	writeTestFile(t, filepath.Join(bin, "curl"), `#!/bin/sh
set -eu
[ "$TEST_FAILURE" != download ] || exit 1
for url do :; done
case "$url" in https://github.com/llm-measurement/fleetdiff/releases/download/v0.3.0/*) ;; *) exit 1 ;; esac
asset=${url##*/}
if [ "$TEST_FAILURE" = missing ] && [ "$asset" = "$TEST_ARCHIVE" ]; then exit 0; fi
cp "$TEST_ASSETS/$asset" "$asset"
`, 0700)
	writeTestFile(t, filepath.Join(bin, "gh"), `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$TEST_LOG"
[ "$#" = 12 ] && [ "$1" = attestation ] && [ "$2" = verify ]
asset=$3
[ "$4" = --bundle ] && [ "$5" = provenance.jsonl ]
[ "$6" = --repo ] && [ "$7" = llm-measurement/fleetdiff ]
[ "$8" = --signer-workflow ] && [ "$9" = llm-measurement/fleetdiff/.github/workflows/release.yml ]
shift 9
[ "$1" = --source-ref ] && [ "$2" = refs/tags/v0.3.0 ] && [ "$3" = --deny-self-hosted-runners ]
if [ "$TEST_FAILURE" = archive ] && [ "$asset" = "$TEST_ARCHIVE" ]; then exit 1; fi
if [ "$TEST_FAILURE" = manifest ] && [ "$asset" = SHA256SUMS ]; then exit 1; fi
`, 0700)
	env := append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"TEST_SYSTEM="+system, "TEST_ARCH="+arch, "TEST_FAILURE="+failure,
		"TEST_ASSETS="+assets, "TEST_ARCHIVE="+name, "TEST_LOG="+filepath.Join(root, "attestations"))
	return root, env
}

func writeTestFile(t *testing.T, path, data string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
}
