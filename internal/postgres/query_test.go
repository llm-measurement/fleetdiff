// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package postgres

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/exportdata"
)

func TestQueriesOnlyUseParsedTimesAndEmbeddedFragments(t *testing.T) {
	for _, details := range []bool{false, true} {
		for _, routing := range []bool{false, true} {
			o := exportdata.Options{Start: time.Date(2026, 10, 7, 0, 0, 0, 123000000, time.FixedZone("SENTINEL_ZONE'); DROP TABLE x; --", 3600)), End: time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC), UsageDetails: details, Routing: routing, Directory: "SENTINEL_PATH';DROP TABLE x;--", CredentialFile: "SENTINEL_CREDENTIAL"}
			q, d, err := queries(o)
			if err != nil {
				t.Fatal(err)
			}
			for _, query := range []string{q, d} {
				if strings.Contains(query, "SENTINEL") || strings.Contains(query, "%L") || strings.Contains(query, ":'start_utc'") {
					t.Fatal("unformatted or caller-controlled SQL")
				}
			}
			if !strings.Contains(q, "'2026-10-06T23:00:00.123Z'") || !strings.Contains(q, `ORDER BY "startTime", request_id COLLATE "C"`) {
				t.Fatal("typed UTC/order contract changed")
			}
			if strings.Contains(q, "AS cache_read_input_tokens") != details || strings.Contains(q, "AS original_model_group") != routing {
				t.Fatal("projection option mismatch")
			}
		}
	}
	if _, _, err := queries(exportdata.Options{}); err == nil {
		t.Fatal("unbounded query accepted")
	}
}

func TestNewOutputAndCleanup(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "export")
	d, err := newOutput(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := d.create("spend.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString("incomplete"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("final output exists before publication")
	}
	if info, err := d.root.Stat("."); err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("directory permissions")
	}
	if info, err := d.root.Stat("spend.csv"); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("file permissions")
	}
	d.close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("incomplete output retained")
	}
	if err := os.Symlink(parent, filepath.Join(parent, "symlink")); err != nil {
		t.Fatal(err)
	}
	if _, err := newOutput(filepath.Join(parent, "symlink", "outside")); err == nil {
		t.Fatal("symlink parent accepted")
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := newOutput(path); err == nil {
		t.Fatal("empty existing directory accepted")
	}
}

func TestOutputByteBound(t *testing.T) {
	var b bytes.Buffer
	w := byteLimit{w: &b, remaining: 3}
	if n, err := w.Write([]byte("abc")); n != 3 || err != nil {
		t.Fatal("exact limit rejected")
	}
	if n, err := w.Write([]byte("d")); n != 0 || !errors.Is(err, errLimit) || b.String() != "abc" {
		t.Fatal("limit truncated or exceeded")
	}
}
