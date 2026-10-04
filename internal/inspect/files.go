// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package inspect

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/llm-measurement/fleetdiff/internal/localfile"
)

const MaxInputBytes = 32 << 20
const MaxFiles = 512
const MaxEntries = 1024
const MaxRecords = 10000
const MaxSpans = 100000
const MaxDimensions = 128

// Each selected file must parse completely. A failed capture never produces a
// partial report. Directory selection is extension-based and nonrecursive.
func readInputs(path string, stdin io.Reader, consume func([]byte, string) error) error {
	if path == "-" {
		if stdin == nil {
			return errors.New("stdin is unavailable")
		}
		b, err := io.ReadAll(io.LimitReader(stdin, MaxInputBytes+1))
		if err != nil {
			return errors.New("cannot read capture from stdin")
		}
		if len(b) > MaxInputBytes {
			return errors.New("capture exceeds 32 MiB")
		}
		return consume(b, "")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return errors.New("cannot inspect capture path")
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return errors.New("capture must be a regular file or directory, not a symlink or special file")
	}
	directory := path
	var names []string
	if !info.IsDir() {
		directory = filepath.Dir(path)
		names = append(names, filepath.Base(path))
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return errors.New("cannot open capture directory")
	}
	defer root.Close()
	if info.IsDir() {
		dir, err := root.Open(".")
		if err != nil {
			return errors.New("cannot read capture directory")
		}
		current, statErr := dir.Stat()
		if statErr != nil || !os.SameFile(info, current) {
			dir.Close()
			return errors.New("capture directory changed while opening")
		}
		entries, readErr := dir.ReadDir(MaxEntries + 1)
		closeErr := dir.Close()
		if (readErr != nil && !errors.Is(readErr, io.EOF)) || closeErr != nil {
			return errors.New("cannot list capture directory")
		}
		if len(entries) > MaxEntries {
			return errors.New("capture directory exceeds 1024 entries")
		}
		for _, entry := range entries {
			switch strings.ToLower(filepath.Ext(entry.Name())) {
			case ".json", ".jsonl", ".otlp", ".pb", ".bin":
				names = append(names, entry.Name())
			}
		}
		slices.Sort(names)
	}
	if len(names) == 0 {
		return errors.New("no capture files selected; use json, jsonl, otlp, pb, or bin files")
	}
	if len(names) > MaxFiles {
		return errors.New("capture exceeds 512 files")
	}
	total := 0
	for _, name := range names {
		b, err := localfile.Read(root, name, MaxInputBytes-total)
		if err != nil {
			return err
		}
		total += len(b)
		if err = consume(b, strings.ToLower(filepath.Ext(name))); err != nil {
			return err
		}
	}
	return nil
}
