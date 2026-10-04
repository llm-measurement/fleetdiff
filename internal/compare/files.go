// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package compare

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/llm-measurement/fleetdiff/internal/localfile"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

const MaxEntries = 1024
const MaxFiles = 512
const MaxInputBytes = 32 << 20

// ReadWindow reads one file or a bounded, nonrecursive directory of canonical
// summary JSON. Diagnostics never copy paths, filenames, or input content.
func ReadWindow(path string, selected *int64) ([]summary.Envelope, error) {
	documents, err := ReadSeries(path)
	if err != nil {
		return nil, err
	}
	var window []summary.Envelope
	for _, doc := range documents {
		if selected != nil && doc.WindowStart != *selected {
			continue
		}
		if len(window) > 0 && doc.WindowStart != window[0].WindowStart {
			return nil, errors.New("input contains multiple windows; select a window explicitly")
		}
		window = append(window, doc)
	}
	if len(window) == 0 {
		return nil, errors.New("no snapshots for the selected window; missing data is not zero")
	}
	return window, nil
}

// ReadSeries uses the same file and total-byte limits as a two-window input.
// It validates every file, including snapshots not selected for a report.
func ReadSeries(path string) ([]summary.Envelope, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, errors.New("cannot inspect input path")
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return nil, errors.New("input must be a regular file or directory, not a symlink or special file")
	}
	directory := path
	names := []string{}
	if !info.IsDir() {
		directory = filepath.Dir(path)
		names = append(names, filepath.Base(path))
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, errors.New("cannot open input directory")
	}
	defer root.Close()
	if info.IsDir() {
		dir, err := root.Open(".")
		if err != nil {
			return nil, errors.New("cannot read input directory")
		}
		current, statErr := dir.Stat()
		if statErr != nil || !os.SameFile(info, current) {
			dir.Close()
			return nil, errors.New("input directory changed while opening")
		}
		entries, readErr := dir.ReadDir(MaxEntries + 1)
		closeErr := dir.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) || closeErr != nil {
			return nil, errors.New("cannot list input directory")
		}
		if len(entries) > MaxEntries {
			return nil, errors.New("input directory exceeds 1024 entries")
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".json") {
				names = append(names, entry.Name())
			}
		}
		slices.Sort(names)
	}
	if len(names) == 0 {
		return nil, errors.New("input contains no summary JSON files")
	}
	if len(names) > MaxFiles {
		return nil, errors.New("input exceeds 512 summary files")
	}
	var documents []summary.Envelope
	total := 0
	for i, name := range names {
		data, err := readRegular(root, name, MaxInputBytes-total)
		if err != nil {
			return nil, fmt.Errorf("input file %d: %w", i+1, err)
		}
		total += len(data)
		doc, err := summary.Parse(data)
		if err != nil {
			return nil, fmt.Errorf("input file %d: invalid or noncanonical summary", i+1)
		}
		documents = append(documents, doc)
	}
	return documents, nil
}

func readRegular(root *os.Root, name string, remaining int) ([]byte, error) {
	return localfile.Read(root, name, min(summary.MaxBytes, remaining))
}
