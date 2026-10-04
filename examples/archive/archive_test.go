// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/compare"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/bloom"
	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

func TestMain(m *testing.M) {
	if os.Getenv("FLEETDIFF_ARCHIVE_TEST_HELPER") == "1" {
		os.Exit(command(os.Args[1:], os.Stdout, os.Stderr, time.Now().UnixNano()))
	}
	os.Exit(m.Run())
}

func realTemp(t testing.TB) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func fixture(start int64) summary.Envelope {
	return summary.Envelope{Version: 1, Sequence: 1, ProducerID: "SENTINEL_PRODUCER", Epoch: "SENTINEL_EPOCH", ScopeID: "SENTINEL_SCOPE", KeyID: "SENTINEL_KEY", AccountingID: "SENTINEL_ACCOUNTING", WindowStart: start, WindowDuration: 60, ObservedStart: start, ObservedEnd: start + 60, EmittedAt: start + 60, Counters: map[string]uint64{"requests": 10}, Sketches: map[string]summary.Payload{}}
}

func encoded(t testing.TB, doc summary.Envelope) []byte {
	t.Helper()
	data, err := doc.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func write(t testing.TB, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func mkdir(t testing.TB, path string) {
	t.Helper()
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
}

func paths(t testing.TB) (string, string) {
	t.Helper()
	parent := realTemp(t)
	source, destination := filepath.Join(parent, "source"), filepath.Join(parent, "archive")
	mkdir(t, source)
	return source, destination
}

func contents(t testing.TB, path string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]string{}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(path, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		result[entry.Name()] = string(data)
	}
	return result
}

func requireSafeError(t testing.TB, err error) {
	t.Helper()
	if err == nil || strings.Contains(err.Error(), "SENTINEL") || strings.ContainsAny(err.Error(), "/\\\n") {
		t.Fatal("missing or unsafe error", err)
	}
}

func TestCumulativeRefreshAndIdempotence(t *testing.T) {
	source, destination := paths(t)
	path := filepath.Join(source, "SENTINEL_SOURCE.json")
	doc := fixture(0)
	doc.ObservedEnd = 40
	for sequence := uint64(1); sequence <= 3; sequence++ {
		doc.Sequence = sequence
		doc.Counters["requests"] = sequence * 10
		doc.EmittedAt = 60 + int64(sequence)
		if sequence > 1 {
			doc.ObservedEnd = 60
		}
		data := encoded(t, doc)
		write(t, path, data) // Collector replaces the same cumulative source file.
		if err := archive(source, destination, 64, 120); err != nil {
			t.Fatal(err)
		}
		files := contents(t, destination)
		if len(files) != 1 || files[archiveName(doc)] != string(data) {
			t.Fatal("snapshot froze, changed bytes, or accumulated old sequences")
		}
	}
	name := filepath.Join(destination, archiveName(doc))
	before, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := archive(source, destination, 64, 120); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(name)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("identical snapshot was rewritten", err)
	}
	if before.Mode().Perm() != 0600 {
		t.Fatal("archive file is not private")
	}
	dir, err := os.Stat(destination)
	if err != nil || dir.Mode().Perm() != 0700 {
		t.Fatal("archive directory is not private", err)
	}
	if strings.Contains(archiveName(doc), "SENTINEL") || !ownName.MatchString(archiveName(doc)) {
		t.Fatal("unsafe filename")
	}
	first, second := doc, doc
	first.ProducerID, first.Epoch = "ab", "c"
	second.ProducerID, second.Epoch = "a", "bc"
	if archiveName(first) == archiveName(second) {
		t.Fatal("ambiguous pair encoding")
	}
}

func TestPartialRestartEpochsRemainUnchanged(t *testing.T) {
	source, destination := paths(t)
	first, restarted := fixture(0), fixture(0)
	first.ObservedEnd = 20
	restarted.Epoch = "restart"
	restarted.ObservedStart = 30
	write(t, filepath.Join(source, "first.json"), encoded(t, first))
	write(t, filepath.Join(source, "restart.json"), encoded(t, restarted))
	if err := archive(source, destination, 64, 120); err != nil {
		t.Fatal(err)
	}
	docs, err := compare.ReadSeries(destination)
	if err != nil || len(docs) != 2 {
		t.Fatal("restart was lost", err)
	}
	combined, err := summary.Combine(docs, []string{first.ProducerID})
	if err != nil || len(combined.Partial) != 1 || combined.Counters["requests"] != 20 {
		t.Fatal("partial restart became complete or double counted", err)
	}
	files := contents(t, destination)
	if files[archiveName(first)] != string(encoded(t, first)) || files[archiveName(restarted)] != string(encoded(t, restarted)) {
		t.Fatal("coverage metadata was rewritten")
	}
	first.Sequence++
	first.ObservedEnd = 30
	write(t, filepath.Join(source, "first.json"), encoded(t, first))
	if err := archive(source, destination, 64, 120); err != nil {
		t.Fatal(err)
	}
	docs, err = compare.ReadSeries(destination)
	if err != nil {
		t.Fatal(err)
	}
	combined, err = summary.Combine(docs, []string{first.ProducerID})
	if err != nil || len(combined.Partial) != 0 {
		t.Fatal("legitimate adjacent restart intervals failed", err)
	}
}

func TestAtomicReplacementKeepsExistingReader(t *testing.T) {
	source, destination := paths(t)
	doc := fixture(0)
	old := encoded(t, doc)
	path := filepath.Join(source, "input.json")
	write(t, path, old)
	if err := archive(source, destination, 64, 120); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(filepath.Join(destination, archiveName(doc)))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	doc.Sequence++
	doc.Counters["requests"]++
	write(t, path, encoded(t, doc))
	if err := archive(source, destination, 64, 120); err != nil {
		t.Fatal(err)
	}
	read, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(old, read) {
		t.Fatal("replacement modified an existing reader's snapshot", err)
	}
	if contents(t, destination)[archiveName(doc)] != string(encoded(t, doc)) {
		t.Fatal("replacement did not publish the new snapshot")
	}
}

func TestFutureAndOpenWindowsIgnored(t *testing.T) {
	source, destination := paths(t)
	closed, futureEmission, open, future := fixture(0), fixture(60), fixture(180), fixture(240)
	futureEmission.EmittedAt = 240
	for i, doc := range []summary.Envelope{closed, futureEmission, open, future} {
		write(t, filepath.Join(source, fmt.Sprintf("%d.json", i)), encoded(t, doc))
	}
	if err := archive(source, destination, 64, 180); err != nil {
		t.Fatal(err)
	}
	files := contents(t, destination)
	if len(files) != 1 || files[archiveName(closed)] != string(encoded(t, closed)) {
		t.Fatal("future or open snapshot was archived")
	}
	// A closed window is eligible exactly at its end, even if observation is partial.
	if err := archive(filepath.Join(source, "0.json"), destination, 64, 60); err != nil {
		t.Fatal(err)
	}
}

func TestConflictsFailWholeBatchBeforeMutation(t *testing.T) {
	tests := map[string]func(*summary.Envelope){
		"equal sequence different bytes": func(d *summary.Envelope) { d.Counters["requests"]++ },
		"older sequence":                 func(d *summary.Envelope) { d.Sequence-- },
		"regressing counter":             func(d *summary.Envelope) { d.Sequence++; d.Counters["requests"]-- },
		"regressing end":                 func(d *summary.Envelope) { d.Sequence++; d.ObservedEnd-- },
		"changed observation start":      func(d *summary.Envelope) { d.Sequence++; d.ObservedStart++ },
		"changed scope":                  func(d *summary.Envelope) { d.Sequence++; d.ScopeID = "other" },
		"changed key":                    func(d *summary.Envelope) { d.Sequence++; d.KeyID = "other" },
		"changed accounting":             func(d *summary.Envelope) { d.Sequence++; d.AccountingID = "other" },
		"changed counters":               func(d *summary.Envelope) { d.Sequence++; d.Counters["other"] = 0 },
		"overlapping restart":            func(d *summary.Envelope) { d.Epoch = "overlap" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			source, destination := paths(t)
			base := fixture(0)
			base.Sequence, base.ObservedEnd = 5, 40
			path := filepath.Join(source, "snapshot.json")
			write(t, path, encoded(t, base))
			if err := archive(source, destination, 64, 180); err != nil {
				t.Fatal(err)
			}
			before := contents(t, destination)
			mutate(&base)
			write(t, path, encoded(t, base))
			write(t, filepath.Join(source, "valid-new-window.json"), encoded(t, fixture(60)))
			requireSafeError(t, archive(source, destination, 1, 180))
			if !reflect.DeepEqual(before, contents(t, destination)) {
				t.Fatal("invalid batch mutated or pruned history")
			}
		})
	}
}

func TestAllSourceSnapshotsValidatedIncludingExcludedWindows(t *testing.T) {
	for _, kind := range []string{"invalid", "noncanonical", "conflicting", "regressing", "open-invalid"} {
		t.Run(kind, func(t *testing.T) {
			source, destination := paths(t)
			doc := fixture(0)
			write(t, filepath.Join(source, "a.json"), encoded(t, doc))
			data := encoded(t, doc)
			switch kind {
			case "invalid", "open-invalid":
				data = []byte(`{"SENTINEL_CONTENT":true}`)
			case "noncanonical":
				data = append(data, '\n')
			case "conflicting":
				doc.Counters["requests"]++
				data = encoded(t, doc)
			case "regressing":
				doc.Sequence++
				doc.Counters["requests"]--
				data = encoded(t, doc)
			}
			write(t, filepath.Join(source, "b.json"), data)
			now := int64(180)
			if kind == "open-invalid" {
				now = 0
			}
			requireSafeError(t, archive(source, destination, 64, now))
			if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid source created an archive", err)
			}
		})
	}
}

func TestSourceDuplicatesSelectNewest(t *testing.T) {
	source, destination := paths(t)
	for i, sequence := range []uint64{3, 1, 3, 2} {
		doc := fixture(0)
		doc.Sequence = sequence
		doc.Counters["requests"] = sequence * 10
		write(t, filepath.Join(source, fmt.Sprintf("%d.json", i)), encoded(t, doc))
	}
	if err := archive(source, destination, 64, 120); err != nil {
		t.Fatal(err)
	}
	docs, err := compare.ReadSeries(destination)
	if err != nil || len(docs) != 1 || docs[0].Sequence != 3 {
		t.Fatal("did not select newest cumulative state", err)
	}
}

func TestRetentionPreservesWholeWindowsAndUnrelatedFiles(t *testing.T) {
	source, destination := paths(t)
	for i := range 66 {
		write(t, filepath.Join(source, fmt.Sprintf("%d.json", i)), encoded(t, fixture(int64(i)*60)))
	}
	var out, diagnostics bytes.Buffer
	if status := command([]string{source, destination}, &out, &diagnostics, 4000); status != 0 || out.Len()+diagnostics.Len() != 0 {
		t.Fatal("default command failed or was not silent", status, diagnostics.String())
	}
	docs, err := compare.ReadSeries(destination)
	if err != nil || len(docs) != 64 || docs[0].WindowStart != 120 {
		t.Fatal("default does not keep newest 64 windows", err)
	}
	write(t, filepath.Join(destination, "unrelated.txt"), []byte("SENTINEL_UNRELATED"))
	partial := fixture(65 * 60)
	partial.ProducerID = "another-owner"
	partial.ObservedStart++
	write(t, filepath.Join(source, "another-owner.json"), encoded(t, partial))
	if err := archive(source, destination, 1, 4000); err != nil {
		t.Fatal(err)
	}
	files := contents(t, destination)
	if len(files) != 3 || files["unrelated.txt"] != "SENTINEL_UNRELATED" || files[archiveName(partial)] != string(encoded(t, partial)) {
		t.Fatal("retention dropped a retained epoch or unrelated file")
	}
	write(t, filepath.Join(destination, "unrelated.json"), []byte("SENTINEL_JSON"))
	before := contents(t, destination)
	requireSafeError(t, archive(source, destination, 1, 4000))
	if !reflect.DeepEqual(before, contents(t, destination)) {
		t.Fatal("unrelated JSON was modified")
	}
}

func TestUnsafeLocationsAndFiles(t *testing.T) {
	for _, kind := range []string{"archive-symlink", "source-symlink", "source-child-symlink", "archive-child-symlink", "source-fifo", "archive-fifo", "archive-directory", "archive-mode", "archive-file-mode", "unsafe-parent", "symlink-parent", "nested", "same", "source-in-archive", "hardlink", "mismatched-name", "corrupt-own-file"} {
		t.Run(kind, func(t *testing.T) {
			source, destination := paths(t)
			doc := fixture(0)
			path := filepath.Join(source, "input.json")
			write(t, path, encoded(t, doc))
			outside := filepath.Join(realTemp(t), "SENTINEL_OUTSIDE")
			write(t, outside, []byte("SENTINEL_UNTOUCHED"))
			m := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "archive-symlink":
				m(os.Symlink(realTemp(t), destination))
			case "source-symlink":
				link := filepath.Join(filepath.Dir(source), "source-link")
				m(os.Symlink(source, link))
				source = link
			case "source-child-symlink":
				m(os.Symlink(outside, filepath.Join(source, "link.json")))
			case "source-fifo":
				m(syscall.Mkfifo(filepath.Join(source, "fifo.json"), 0600))
			case "unsafe-parent":
				parent := filepath.Join(filepath.Dir(source), "unsafe")
				mkdir(t, parent)
				m(os.Chmod(parent, 0777))
				destination = filepath.Join(parent, "archive")
			case "symlink-parent":
				parent := filepath.Join(filepath.Dir(source), "link")
				m(os.Symlink(realTemp(t), parent))
				destination = filepath.Join(parent, "archive")
			case "nested":
				destination = filepath.Join(source, "archive")
			case "same":
				destination = source
			case "source-in-archive":
				destination = filepath.Dir(source)
			default:
				mkdir(t, destination)
				name := filepath.Join(destination, archiveName(doc))
				switch kind {
				case "archive-child-symlink":
					m(os.Symlink(outside, name))
				case "archive-fifo":
					m(syscall.Mkfifo(name, 0600))
				case "archive-directory":
					mkdir(t, name)
				case "archive-mode":
					m(os.Chmod(destination, 0755))
				case "archive-file-mode":
					write(t, name, encoded(t, doc))
					m(os.Chmod(name, 0644))
				case "hardlink":
					m(os.Link(path, name))
				case "mismatched-name":
					doc.Epoch = "different"
					write(t, name, encoded(t, doc))
				case "corrupt-own-file":
					write(t, name, []byte("SENTINEL_BAD_SUMMARY"))
				}
			}
			requireSafeError(t, archive(source, destination, 64, 180))
			data, err := os.ReadFile(outside)
			if err != nil || string(data) != "SENTINEL_UNTOUCHED" {
				t.Fatal("outside file changed", err)
			}
		})
	}
}

func TestInputAndArchiveBounds(t *testing.T) {
	for _, kind := range []string{"input-entries", "input-files", "input-file-bytes", "archive-entries", "archive-files", "archive-bytes"} {
		t.Run(kind, func(t *testing.T) {
			source, destination := paths(t)
			write(t, filepath.Join(source, "input.json"), encoded(t, fixture(0)))
			mkdir(t, destination)
			switch kind {
			case "input-entries", "archive-entries":
				dir := source
				if kind == "archive-entries" {
					dir = destination
				}
				for i := range compare.MaxEntries + 1 {
					write(t, filepath.Join(dir, fmt.Sprintf("entry-%d", i)), nil)
				}
			case "input-files":
				for i := range compare.MaxFiles {
					write(t, filepath.Join(source, fmt.Sprintf("%d.json", i)), encoded(t, fixture(0)))
				}
			case "archive-files":
				for i := range compare.MaxFiles + 1 {
					doc := fixture(int64(i) * 60)
					write(t, filepath.Join(destination, archiveName(doc)), encoded(t, doc))
				}
			case "input-file-bytes":
				write(t, filepath.Join(source, "input.json"), bytes.Repeat([]byte("x"), summary.MaxBytes+1))
			case "archive-bytes":
				f, err := os.Create(filepath.Join(destination, "unrelated-large-file"))
				if err != nil {
					t.Fatal(err)
				}
				if err := errors.Join(f.Truncate(compare.MaxInputBytes+1), f.Close()); err != nil {
					t.Fatal(err)
				}
			}
			requireSafeError(t, archive(source, destination, 64, 40000))
		})
	}
}

func TestAggregateInputByteLimit(t *testing.T) {
	source, destination := paths(t)
	sketch, err := bloom.New(bloom.ProfileDefault, sketchhash.PromptV1, sketchhash.HMACSHA25664)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := sketch.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	doc := fixture(0)
	doc.Sketches["large_fixture"] = summary.Payload{Kind: "bloom", Data: payload}
	data := encoded(t, doc)
	n := compare.MaxInputBytes/len(data) + 1
	if n > compare.MaxFiles || len(data) > summary.MaxBytes {
		t.Fatal("fixture must exceed only the aggregate byte budget")
	}
	for i := range n {
		write(t, filepath.Join(source, fmt.Sprintf("%03d.json", i)), data)
	}
	// Confirm canonical data passes before checking that the aggregate is bounded.
	if _, err := compare.ReadSeries(filepath.Join(source, "000.json")); err != nil {
		t.Fatal(err)
	}
	requireSafeError(t, archive(source, destination, 64, 120))
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("over-budget input created an archive", err)
	}
}

func TestPlannedBudgetsCheckedBeforePruning(t *testing.T) {
	for _, kind := range []string{"staged-bytes", "staged-entries", "transient-files", "retained-epochs"} {
		t.Run(kind, func(t *testing.T) {
			old := fixture(0)
			data := encoded(t, old)
			state := inventory{files: map[string]snapshot{archiveName(old): {doc: old, data: data}}, entries: 1, bytes: int64(len(data))}
			input := []summary.Envelope{fixture(60)}
			switch kind {
			case "staged-bytes":
				state.bytes = compare.MaxInputBytes
			case "staged-entries":
				state.entries = compare.MaxEntries
			case "transient-files":
				for i := 1; i < compare.MaxFiles; i++ {
					doc := fixture(int64(i) * 60)
					state.files[archiveName(doc)] = snapshot{doc: doc, data: encoded(t, doc)}
				}
				input = []summary.Envelope{fixture(int64(compare.MaxFiles) * 60)}
			case "retained-epochs":
				state.files = map[string]snapshot{}
				for i := range compare.MaxFiles + 1 {
					doc := fixture(0)
					doc.Epoch = fmt.Sprintf("restart-%d", i)
					doc.WindowDuration, doc.EmittedAt = 600, 600
					doc.ObservedStart, doc.ObservedEnd = int64(i), int64(i+1)
					if i == compare.MaxFiles {
						input = []summary.Envelope{doc}
					} else {
						state.files[archiveName(doc)] = snapshot{doc: doc, data: encoded(t, doc)}
					}
				}
			}
			_, err := planArchive(state, input, 1, 40000)
			requireSafeError(t, err)
			if !strings.Contains(err.Error(), "budget") {
				t.Fatal("fixture failed before reaching budget checks", err)
			}
		})
	}
}

func TestStagingFailurePreservesAllHistory(t *testing.T) {
	source, destination := paths(t)
	write(t, filepath.Join(source, "old.json"), encoded(t, fixture(0)))
	if err := archive(source, destination, 64, 240); err != nil {
		t.Fatal(err)
	}
	before := contents(t, destination)
	root, err := openArchive(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	dir, err := lockArchive(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	current, err := readArchive(root)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planArchive(current, []summary.Envelope{fixture(60), fixture(120)}, 2, 240)
	if err != nil || len(plan.remove) != 1 || len(plan.writes) != 2 {
		t.Fatal("test did not plan both staging and pruning", err)
	}
	calls := 0
	failSecond := func(root *os.Root, data []byte) (stagedFile, error) {
		calls++
		if calls == 2 {
			return stagedFile{}, syscall.ENOSPC
		}
		return stageFile(root, data)
	}
	requireSafeError(t, installPlan(root, dir, current, plan, failSecond))
	if calls != 2 || !reflect.DeepEqual(before, contents(t, destination)) {
		t.Fatal("staging failure pruned history, installed part of a batch, or left temporary files")
	}
}

func TestArchiveLockAndRevalidation(t *testing.T) {
	source, destination := paths(t)
	write(t, filepath.Join(source, "old.json"), encoded(t, fixture(0)))
	if err := archive(source, destination, 64, 240); err != nil {
		t.Fatal(err)
	}
	root, err := openArchive(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	dir, err := lockArchive(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	requireSafeError(t, archive(source, destination, 64, 240))
	current, err := readArchive(root)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planArchive(current, []summary.Envelope{fixture(60)}, 1, 240)
	if err != nil {
		t.Fatal(err)
	}
	changed := fixture(0)
	changed.Sequence++
	replacement := encoded(t, changed)
	replaceWhileStaging := func(root *os.Root, data []byte) (stagedFile, error) {
		write(t, filepath.Join(destination, archiveName(changed)), replacement)
		return stageFile(root, data)
	}
	requireSafeError(t, installPlan(root, dir, current, plan, replaceWhileStaging))
	files := contents(t, destination)
	if len(files) != 1 || files[archiveName(changed)] != string(replacement) {
		t.Fatal("changed history was deleted or new batch installed")
	}
}

func TestCommandAndWrapperNeverPrintPrivateValues(t *testing.T) {
	source, destination := paths(t)
	write(t, filepath.Join(source, "input.json"), encoded(t, fixture(0)))
	for _, args := range [][]string{
		{"--help"},
		{source, destination, "--keep-windows", "384"},
		{source, destination, "--keep-windows", "0"},
		{source, destination, "--keep-windows", "385"},
		{source, destination, "--keep-windows", "SENTINEL_VALUE"},
		{source, destination, "--SENTINEL_OPTION"},
		{"SENTINEL_PATH", destination},
	} {
		var out, diagnostics bytes.Buffer
		status := command(args, &out, &diagnostics, 120)
		if strings.Contains(out.String()+diagnostics.String(), "SENTINEL") {
			t.Fatal("command leaked input")
		}
		if status == 0 && len(args) > 1 && out.Len()+diagnostics.Len() != 0 {
			t.Fatal("success was not silent")
		}
	}
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, missing := range []bool{false, true} {
		path := helper
		if missing {
			path = filepath.Join(realTemp(t), "SENTINEL_MISSING_BINARY")
		}
		cmd := exec.Command("sh", "../archive.sh", source, destination)
		cmd.Env = append(os.Environ(), "FLEETDIFF_ARCHIVE_HELPER="+path, "FLEETDIFF_ARCHIVE_TEST_HELPER=1")
		output, err := cmd.CombinedOutput()
		if (err != nil) != missing || strings.Contains(string(output), "SENTINEL") || (!missing && len(output) != 0) {
			t.Fatal("wrapper failed or leaked diagnostics", string(output), err)
		}
	}
}
