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
	"syscall"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

const MaxEntries = 1024
const MaxFiles = 512
const MaxInputBytes = 32 << 20

// ReadWindow reads one file or a bounded, nonrecursive directory of canonical
// summary JSON. Diagnostics never copy paths, filenames, or input content.
func ReadWindow(path string, selected *int64) ([]summary.Envelope, error) {
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
		if selected != nil && doc.WindowStart != *selected {
			continue
		}
		if len(documents) > 0 && doc.WindowStart != documents[0].WindowStart {
			return nil, errors.New("input contains multiple windows; select a window explicitly")
		}
		documents = append(documents, doc)
	}
	if len(documents) == 0 {
		return nil, errors.New("no snapshots for the selected window; missing data is not zero")
	}
	return documents, nil
}

func readRegular(root *os.Root, name string, remaining int) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("expected a regular file, not a symlink or special file")
	}
	if info.Size() > summary.MaxBytes {
		return nil, errors.New("summary exceeds 8 MiB")
	}
	if info.Size() > int64(remaining) {
		return nil, errors.New("input exceeds 32 MiB")
	}
	// Nonblocking/no-follow protects the final component against a replacement
	// with a FIFO or symlink between Lstat and open. Root confines traversal.
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errors.New("cannot open summary file")
	}
	defer f.Close()
	current, err := f.Stat()
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(info, current) {
		return nil, errors.New("summary file changed while opening")
	}
	limit := min(summary.MaxBytes, remaining)
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, errors.New("cannot read summary file")
	}
	if len(data) > limit {
		return nil, errors.New("summary or input size limit exceeded")
	}
	return data, nil
}
