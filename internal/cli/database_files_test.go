// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/exportdata"
)

func TestDatabaseTemporaryCleanupPreservesReplacement(t *testing.T) {
	for _, kind := range []string{"file", "directory", "symlink"} {
		for _, failure := range []string{"export", "analysis"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				base, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				t.Setenv("TMPDIR", base)
				target := filepath.Join(base, "unrelated")
				if err := os.Mkdir(target, 0700); err != nil {
					t.Fatal(err)
				}
				sentinel := filepath.Join(target, "sentinel")
				if err := os.WriteFile(sentinel, []byte("unrelated contents"), 0600); err != nil {
					t.Fatal(err)
				}
				var original, moved string
				var replacement os.FileInfo
				code, out, errs := databaseRun([]string{"investigate", "--litellm-db"}, func(_ context.Context, opts exportdata.Options) (exportdata.Result, error) {
					result := databaseFixture(t, opts, []byte("invalid CSV"), nil)
					original = filepath.Dir(opts.Directory)
					moved = original + "-moved"
					if err := os.Rename(original, moved); err != nil {
						t.Fatal(err)
					}
					switch kind {
					case "file":
						err = os.WriteFile(original, []byte("replacement"), 0600)
					case "directory":
						err = os.Mkdir(original, 0700)
						if err == nil {
							err = os.WriteFile(filepath.Join(original, "sentinel"), []byte("replacement"), 0600)
						}
					case "symlink":
						err = os.Symlink(target, original)
					}
					if err != nil {
						t.Fatal(err)
					}
					replacement, err = os.Lstat(original)
					if err != nil {
						t.Fatal(err)
					}
					if failure == "export" {
						return result, errors.New("SENTINEL exporter error")
					}
					result.SpendPath = filepath.Join(moved, "export", "spend.csv")
					return result, nil
				})
				if code != 1 || out != "" || strings.Contains(errs, "SENTINEL") || strings.Contains(errs, base) {
					t.Fatalf("exit=%d out=%q errors=%q", code, out, errs)
				}
				after, err := os.Lstat(original)
				if err != nil || !os.SameFile(replacement, after) {
					t.Fatalf("cleanup removed replacement: %v", err)
				}
				data, err := os.ReadFile(sentinel)
				if err != nil || string(data) != "unrelated contents" {
					t.Fatalf("cleanup touched symlink target: %q %v", data, err)
				}
				if kind == "directory" {
					data, err := os.ReadFile(filepath.Join(original, "sentinel"))
					if err != nil || string(data) != "replacement" {
						t.Fatalf("cleanup traversed replacement: %q %v", data, err)
					}
				}
				if _, err := os.Lstat(filepath.Join(moved, "export")); !os.IsNotExist(err) {
					t.Fatalf("owned export was not cleaned through held root: %v", err)
				}
			})
		}
	}
}

func TestDatabaseTemporaryParentPolicy(t *testing.T) {
	for _, ancestor := range []bool{false, true} {
		base := t.TempDir()
		if err := os.Chmod(base, 0777); err != nil {
			t.Fatal(err)
		}
		if ancestor {
			base = filepath.Join(base, "private")
			if err := os.Mkdir(base, 0700); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv("TMPDIR", base)
		code, out, errs := databaseRun([]string{"investigate", "--litellm-db"}, func(context.Context, exportdata.Options) (exportdata.Result, error) {
			t.Fatal("untrusted temporary parent reached exporter")
			return exportdata.Result{}, nil
		})
		if code != 1 || out != "" || !strings.Contains(errs, "trusted parent") || strings.Contains(errs, base) {
			t.Fatalf("exit=%d out=%q errors=%q", code, out, errs)
		}
	}
}
