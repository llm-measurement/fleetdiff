// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/llm-measurement/fleetdiff/internal/compare"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

type snapshot struct {
	doc  summary.Envelope
	data []byte
	info os.FileInfo
}

type inventory struct {
	files   map[string]snapshot
	entries int
	bytes   int64 // Includes unrelated regular files, which are never removed.
}

type archivePlan struct {
	writes map[string]snapshot
	remove []string
}

func archiveName(doc summary.Envelope) string {
	// Identifiers cannot contain NUL, so the pair encoding is unambiguous.
	digest := sha256.Sum256([]byte(doc.ProducerID + "\x00" + doc.Epoch))
	return fmt.Sprintf("fleetdiff-archive-v1-%020d-%x.json", doc.WindowStart, digest)
}

func validateSeries(documents []summary.Envelope) error {
	groups := map[int64][]summary.Envelope{}
	for _, doc := range documents {
		if err := summary.Compatible(documents[0], doc); err != nil {
			return errors.New("incompatible summary history")
		}
		groups[doc.WindowStart] = append(groups[doc.WindowStart], doc)
	}
	for _, group := range groups {
		owners := map[string]bool{}
		var expected []string
		for _, doc := range group {
			if !owners[doc.ProducerID] {
				expected = append(expected, doc.ProducerID)
				owners[doc.ProducerID] = true
			}
		}
		// This is integrity validation, not an assertion about expected coverage.
		if _, err := summary.Combine(group, expected); err != nil {
			return errors.New("conflicting, regressing, or incompatible summary history")
		}
	}
	return nil
}

func planArchive(current inventory, input []summary.Envelope, keep int, now int64) (archivePlan, error) {
	plan := archivePlan{writes: map[string]snapshot{}}
	all := append([]summary.Envelope(nil), input...)
	selected := map[string]snapshot{}
	for name, old := range current.files {
		if old.doc.WindowStart+old.doc.WindowDuration > now || old.doc.EmittedAt > now {
			return plan, errors.New("archive contains snapshots after the reference time; check clocks")
		}
		all = append(all, old.doc)
		selected[name] = old
	}
	if err := validateSeries(all); err != nil {
		return plan, err
	}
	for _, doc := range input {
		if doc.WindowStart+doc.WindowDuration > now || doc.EmittedAt > now {
			continue
		}
		name := archiveName(doc)
		if old, ok := current.files[name]; ok && doc.Sequence < old.doc.Sequence {
			return plan, errors.New("source sequence is older than archived history")
		}
		if old, ok := selected[name]; ok && doc.Sequence <= old.doc.Sequence {
			continue
		}
		data, err := doc.MarshalBinary()
		if err != nil {
			return plan, errors.New("invalid summary snapshot")
		}
		selected[name] = snapshot{doc: doc, data: data}
	}
	starts := map[int64]bool{}
	var windows []int64
	for _, file := range selected {
		if !starts[file.doc.WindowStart] {
			starts[file.doc.WindowStart] = true
			windows = append(windows, file.doc.WindowStart)
		}
	}
	slices.Sort(windows)
	for _, start := range windows[:max(0, len(windows)-keep)] {
		delete(starts, start)
	}
	finalBytes, stagingBytes := current.bytes, int64(0)
	finalFiles, newFiles := len(current.files), 0
	for name, file := range selected {
		old, existed := current.files[name]
		if !starts[file.doc.WindowStart] {
			if existed {
				plan.remove = append(plan.remove, name)
				finalBytes -= int64(len(old.data))
				finalFiles--
			}
			continue
		}
		if existed && bytes.Equal(old.data, file.data) {
			continue
		}
		plan.writes[name] = file
		stagingBytes += int64(len(file.data))
		finalBytes += int64(len(file.data) - len(old.data))
		if !existed {
			newFiles++
			finalFiles++
		}
	}
	// Reserve the entire staged batch without reclaiming any old history first.
	if finalFiles > compare.MaxFiles || len(current.files)+newFiles > compare.MaxFiles ||
		current.entries+len(plan.writes) > compare.MaxEntries ||
		finalBytes > compare.MaxInputBytes || current.bytes+stagingBytes > compare.MaxInputBytes {
		return archivePlan{}, errors.New("archive or staging budget exceeded; reduce retention or use a separate archive")
	}
	slices.Sort(plan.remove)
	return plan, nil
}
