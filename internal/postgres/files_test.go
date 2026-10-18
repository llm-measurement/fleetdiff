// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package postgres

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	process "os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/exportdata"
	"github.com/llm-measurement/fleetdiff/internal/spend"
)

const stagedSpend = "request_id,call_type,startTime,prompt_tokens,completion_tokens,total_tokens\nprivate-id,completion,2026-10-01T01:00:00Z,1,2,3\n"
const stagedDaily = "date,api_requests,successful_requests,failed_requests\n2026-10-01,1,1,0\n"

func privateOutputPath(t *testing.T) string {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(parent, "export")
}

func stageOutput(t *testing.T, path string) *outputDir {
	t.Helper()
	d, err := newOutput(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.close)
	for name, body := range map[string]string{"spend.csv": stagedSpend, "daily.csv": stagedDaily} {
		f, err := d.create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.WriteString(body); err != nil {
			t.Fatal(err)
		}
		if err = f.Sync(); err != nil {
			t.Fatal(err)
		}
		if err = f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func assertCompleteOutput(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 2 {
		t.Fatalf("not one complete two-file export: %v %v", entries, err)
	}
	for name, want := range map[string]string{"spend.csv": stagedSpend, "daily.csv": stagedDaily} {
		got, err := os.ReadFile(filepath.Join(path, name))
		if err != nil || string(got) != want {
			t.Fatalf("incomplete %s: %q %v", name, got, err)
		}
		info, err := os.Stat(filepath.Join(path, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("file is not private: %v %v", info, err)
		}
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("directory is not private: %v %v", info, err)
	}
}

func makeReplacement(t *testing.T, path, kind string) {
	t.Helper()
	switch kind {
	case "file":
		if err := os.WriteFile(path, []byte("unrelated sentinel"), 0600); err != nil {
			t.Fatal(err)
		}
	case "empty-directory", "directory":
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if kind == "directory" {
			if err := os.WriteFile(filepath.Join(path, "sentinel"), []byte("unrelated sentinel"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	case "symlink":
		if err := os.Symlink("not-an-export", path); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown replacement kind %q", kind)
	}
}

func assertReplacement(t *testing.T, path, kind string, before os.FileInfo) {
	t.Helper()
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("replacement changed: %s %v", kind, err)
	}
	if kind == "file" || kind == "directory" {
		if kind == "directory" {
			path = filepath.Join(path, "sentinel")
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "unrelated sentinel" {
			t.Fatalf("replacement data changed: %s %q %v", kind, data, err)
		}
	}
}

func TestOutputDirectoryAtomicNoReplace(t *testing.T) {
	for _, kind := range []string{"absent", "file", "empty-directory", "directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			path := privateOutputPath(t)
			d := stageOutput(t, path)
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatal("staging exposed final output")
			}
			var before os.FileInfo
			if kind != "absent" {
				makeReplacement(t, path, kind)
				before, _ = os.Lstat(path)
			}
			err := d.publish(context.Background())
			if kind == "absent" {
				if err != nil || !d.complete {
					t.Fatalf("publish: %v", err)
				}
				d.close()
				assertCompleteOutput(t, path)
			} else {
				if !errors.Is(err, errOutput) || d.complete {
					t.Fatalf("existing destination accepted: %v", err)
				}
				d.close()
				assertReplacement(t, path, kind, before)
			}
		})
	}
}

func TestOutputRejectsExistingBeforeStaging(t *testing.T) {
	for _, kind := range []string{"file", "empty-directory", "directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			path := privateOutputPath(t)
			makeReplacement(t, path, kind)
			before, _ := os.Lstat(path)
			if _, err := newOutput(path); !errors.Is(err, errOutput) {
				t.Fatalf("existing output accepted: %v", err)
			}
			assertReplacement(t, path, kind, before)
			entries, err := os.ReadDir(filepath.Dir(path))
			if err != nil || len(entries) != 1 {
				t.Fatalf("existing output created staging directory: %v %v", entries, err)
			}
		})
	}
}

func TestOutputCleanupOnlyRemovesEmptyDirectory(t *testing.T) {
	for _, kind := range []string{"file", "empty-directory", "directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			path := privateOutputPath(t)
			makeReplacement(t, path, kind)
			before, _ := os.Lstat(path)
			parent, err := os.Open(filepath.Dir(path))
			if err != nil {
				t.Fatal(err)
			}
			defer parent.Close()
			err = removeEmptyDirectoryAt(parent, filepath.Base(path))
			if kind == "empty-directory" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatal("empty directory not removed")
				}
			} else {
				if err == nil {
					t.Fatal("directory-only cleanup accepted another file type or a nonempty directory")
				}
				assertReplacement(t, path, kind, before)
			}
		})
	}
}

func TestOutputWrapperReplacementCannotRedirectValidationOrPublication(t *testing.T) {
	for _, publish := range []bool{false, true} {
		for _, kind := range []string{"file", "empty-directory", "directory", "symlink"} {
			t.Run(fmt.Sprintf("publish=%t/%s", publish, kind), func(t *testing.T) {
				path := privateOutputPath(t)
				d := stageOutput(t, path)
				original := filepath.Join(filepath.Dir(path), d.stage)
				moved := original + "-moved"
				if err := os.Rename(original, moved); err != nil {
					t.Fatal(err)
				}
				makeReplacement(t, original, kind)
				before, _ := os.Lstat(original)
				rows := 0
				if err := spend.ReadRoutingRoot(d.root, "spend.csv", func(spend.Row) error { rows++; return nil }); err != nil || rows != 1 {
					t.Fatalf("held-root spend validation redirected: %d %v", rows, err)
				}
				if err := spend.ValidateDailyExportRoot(d.root, "daily.csv"); err != nil {
					t.Fatalf("held-root daily validation redirected: %v", err)
				}
				if publish {
					if err := d.publish(context.Background()); err != nil {
						t.Fatal(err)
					}
				}
				d.close()
				assertReplacement(t, original, kind, before)
				if publish {
					assertCompleteOutput(t, path)
				} else {
					if _, err := os.Lstat(path); !os.IsNotExist(err) {
						t.Fatal("failed export exposed final path")
					}
					if _, err := os.Lstat(filepath.Join(moved, "export", "spend.csv")); !os.IsNotExist(err) {
						t.Fatal("private staged data not cleaned through held root")
					}
				}
			})
		}
	}
}

func TestOutputParentAndInnerSourceReplacementsFailClosed(t *testing.T) {
	for _, source := range []bool{false, true} {
		path := privateOutputPath(t)
		d := stageOutput(t, path)
		if source {
			if err := d.container.Rename("export", "moved"); err != nil {
				t.Fatal(err)
			}
			if err := d.container.Mkdir("export", 0700); err != nil {
				t.Fatal(err)
			}
		} else {
			parent := filepath.Dir(path)
			if err := os.Rename(parent, parent+"-moved"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(parent + "-moved") })
			if err := os.Mkdir(parent, 0700); err != nil {
				t.Fatal(err)
			}
		}
		if err := d.publish(context.Background()); !errors.Is(err, errOutput) {
			t.Fatalf("replaced source/parent accepted: %v", err)
		}
		d.close()
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("replacement path received an export")
		}
	}
}

func TestOutputParentPolicyIsOutputOnly(t *testing.T) {
	path := privateOutputPath(t)
	parent := filepath.Dir(path)
	for _, mode := range []os.FileMode{0777, 0770} {
		if err := os.Chmod(parent, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := newOutput(path); !errors.Is(err, errOutput) {
			t.Fatalf("untrusted writable parent accepted: %o %v", mode, err)
		}
		root, _, err := openParent(path)
		if err != nil {
			t.Fatalf("credential parent policy changed: %v", err)
		}
		root.Close()
	}
	if err := os.Chmod(parent, 0777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	d, err := newOutput(path)
	if err != nil {
		t.Fatalf("trusted sticky parent rejected: %v", err)
	}
	d.close()
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	ancestor := filepath.Join(parent, "untrusted")
	if err := os.Mkdir(ancestor, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ancestor, 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(ancestor, "private"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := newOutput(filepath.Join(ancestor, "private", "export")); !errors.Is(err, errOutput) {
		t.Fatal("untrusted ancestor accepted")
	}
}

func TestOutputCancellationAndCommitPoint(t *testing.T) {
	path := privateOutputPath(t)
	d := stageOutput(t, path)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.publish(ctx); !errors.Is(err, exportdata.ErrInterrupted) {
		t.Fatalf("canceled publication: %v", err)
	}
	d.close()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("canceled export was published")
	}
	d = stageOutput(t, path)
	ctx, cancel = context.WithCancel(context.Background())
	if err := d.publish(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	d.close()
	assertCompleteOutput(t, path)
}

func TestOutputHeldRootRejectsSymlinksAndInvalidData(t *testing.T) {
	path := privateOutputPath(t)
	d := stageOutput(t, path)
	if err := d.root.Rename("spend.csv", "moved.csv"); err != nil {
		t.Fatal(err)
	}
	if err := d.root.Symlink("moved.csv", "spend.csv"); err != nil {
		t.Fatal(err)
	}
	if err := spend.ReadRoutingRoot(d.root, "spend.csv", func(spend.Row) error { return nil }); err == nil {
		t.Fatal("held-root validator accepted symlink")
	}
	if err := d.root.WriteFile("daily.csv", []byte("invalid daily"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := spend.ValidateDailyExportRoot(d.root, "daily.csv"); err == nil {
		t.Fatal("held-root validator skipped daily parser")
	}
}

func TestOutputKillPublicationIsAllOrNothing(t *testing.T) {
	for _, phase := range []string{"staged", "committing", "published"} {
		t.Run(phase, func(t *testing.T) {
			path := privateOutputPath(t)
			cmd := process.Command(os.Args[0], "-test.run=^TestOutputProcessHelper$")
			cmd.Env = append(os.Environ(), "FLEETDIFF_OUTPUT_TEST_PATH="+path, "FLEETDIFF_OUTPUT_TEST_PHASE="+phase)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer cmd.Process.Kill()
			line, err := bufio.NewReader(stdout).ReadString('\n')
			if err != nil || strings.TrimSpace(line) != "ready" {
				t.Fatalf("subprocess did not stage: %q %v", line, err)
			}
			if phase == "committing" {
				_, _ = input.Write([]byte{1})
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err == nil {
				t.Fatal("subprocess was not interrupted")
			}
			_, err = os.Lstat(path)
			if phase == "staged" && !os.IsNotExist(err) {
				t.Fatal("staging created final directory")
			}
			if phase == "published" || err == nil {
				assertCompleteOutput(t, path)
			}
		})
	}
}

func TestOutputProcessHelper(t *testing.T) {
	path := os.Getenv("FLEETDIFF_OUTPUT_TEST_PATH")
	if path == "" {
		return
	}
	d := stageOutput(t, path)
	phase := os.Getenv("FLEETDIFF_OUTPUT_TEST_PHASE")
	if phase == "published" {
		if err := d.publish(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	fmt.Println("ready")
	var b [1]byte
	if _, err := io.ReadFull(os.Stdin, b[:]); err != nil {
		t.Fatal(err)
	}
	if phase == "committing" {
		if err := d.publish(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = io.ReadFull(os.Stdin, b[:])
}
