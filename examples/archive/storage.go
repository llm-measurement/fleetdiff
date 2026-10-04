// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package main

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"

	"github.com/llm-measurement/fleetdiff/internal/compare"
	"github.com/llm-measurement/fleetdiff/internal/localfile"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

var ownName = regexp.MustCompile(`^fleetdiff-archive-v1-[0-9]{20}-[a-f0-9]{64}\.json$`)

func private(info os.FileInfo, mode os.FileMode) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && info.Mode().Perm() == mode &&
		info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0
}

// Walk real directories without following symlinks. Root-owned sticky temporary
// ancestors are allowed; other group/other-writable ancestors are unsafe.
func safeDirectory(path string) (*os.Root, error) {
	root, err := os.OpenRoot(string(filepath.Separator))
	if err != nil {
		return nil, errors.New("cannot open trusted directory")
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		info, err := root.Lstat(part)
		if err != nil || !info.IsDir() {
			root.Close()
			return nil, errors.New("path contains a missing, symlink, or non-directory parent")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || (stat.Uid != 0 && stat.Uid != uint32(os.Geteuid())) ||
			(info.Mode().Perm()&0022 != 0 && !(stat.Uid == 0 && info.Mode()&os.ModeSticky != 0)) {
			root.Close()
			return nil, errors.New("path contains an unsafe parent")
		}
		next, openErr := root.OpenRoot(part)
		root.Close()
		if openErr != nil {
			return nil, errors.New("cannot open trusted directory")
		}
		opened, statErr := next.Stat(".")
		if statErr != nil || !os.SameFile(info, opened) {
			next.Close()
			return nil, errors.New("directory changed while opening")
		}
		root = next
	}
	return root, nil
}

func absolutePath(path string) (string, error) {
	if path == "" || strings.ContainsRune(path, 0) || slices.Contains(strings.Split(path, string(filepath.Separator)), "..") {
		return "", errors.New("invalid path; use real paths without parent traversal")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", errors.New("cannot resolve local path")
	}
	return path, nil
}

func checkPaths(source, destination string) (string, string, error) {
	source, err := absolutePath(source)
	if err != nil {
		return "", "", err
	}
	destination, err = absolutePath(destination)
	if err != nil {
		return "", "", err
	}
	if source == destination || strings.HasPrefix(source, destination+string(filepath.Separator)) ||
		strings.HasPrefix(destination, source+string(filepath.Separator)) || destination == string(filepath.Separator) {
		return "", "", errors.New("source and archive must be separate, non-nested locations")
	}
	parent, err := safeDirectory(filepath.Dir(source))
	if err != nil {
		return "", "", err
	}
	defer parent.Close()
	info, err := parent.Lstat(filepath.Base(source))
	if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
		return "", "", errors.New("source must be a regular file or directory")
	}
	if info.IsDir() {
		dir, err := safeDirectory(source)
		if err != nil {
			return "", "", err
		}
		dir.Close()
	}
	return source, destination, nil
}

func openArchive(path string) (*os.Root, error) {
	parent, err := safeDirectory(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	name := filepath.Base(path)
	if err := parent.Mkdir(name, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, errors.New("cannot create private archive")
	}
	info, err := parent.Lstat(name)
	if err != nil || !info.IsDir() || !private(info, 0700) {
		return nil, errors.New("archive must be an owned private directory with mode 0700")
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, errors.New("cannot open archive")
	}
	current, err := root.Stat(".")
	if err != nil || !os.SameFile(info, current) || !private(current, 0700) {
		root.Close()
		return nil, errors.New("archive changed while opening")
	}
	return root, nil
}

func lockArchive(root *os.Root) (*os.File, error) {
	dir, err := root.Open(".")
	if err != nil {
		return nil, errors.New("cannot open archive for locking")
	}
	if err := syscall.Flock(int(dir.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		dir.Close()
		return nil, errors.New("archive is busy or does not support local locking")
	}
	return dir, nil
}

func readArchive(root *os.Root) (inventory, error) {
	state := inventory{files: map[string]snapshot{}}
	dir, err := root.Open(".")
	if err != nil {
		return state, errors.New("cannot list archive")
	}
	entries, err := dir.ReadDir(compare.MaxEntries + 1)
	closeErr := dir.Close()
	if err != nil && !errors.Is(err, io.EOF) || closeErr != nil || len(entries) > compare.MaxEntries {
		return state, errors.New("archive is unreadable or exceeds entry limit")
	}
	state.entries = len(entries)
	for _, entry := range entries {
		name := entry.Name()
		info, err := root.Lstat(name)
		if err != nil || !info.Mode().IsRegular() {
			return state, errors.New("archive contains a symlink, special file, or directory")
		}
		if info.Size() > compare.MaxInputBytes-state.bytes {
			return state, errors.New("archive exceeds byte limit")
		}
		state.bytes += info.Size()
		if !ownName.MatchString(name) {
			if strings.HasSuffix(name, ".json") {
				return state, errors.New("archive contains unrelated JSON; use a separate archive")
			}
			continue
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !private(info, 0600) || !ok || stat.Nlink != 1 || len(state.files) >= compare.MaxFiles {
			return state, errors.New("archive file is not private or exceeds file limit")
		}
		data, err := localfile.Read(root, name, summary.MaxBytes)
		if err != nil || int64(len(data)) != info.Size() {
			return state, errors.New("cannot safely read archive file")
		}
		doc, err := summary.Parse(data)
		if err != nil || archiveName(doc) != name {
			return state, errors.New("archive contains an invalid or unrecognized snapshot")
		}
		state.files[name] = snapshot{doc: doc, data: data, info: info}
	}
	return state, nil
}

type stagedFile struct {
	name string
	info os.FileInfo
}

func stageFile(root *os.Root, data []byte) (stagedFile, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return stagedFile{}, errors.New("cannot prepare archive staging")
	}
	stage := stagedFile{name: fmt.Sprintf(".fleetdiff-archive-stage-%x", nonce)}
	f, err := root.OpenFile(stage.name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return stagedFile{}, errors.New("cannot create staged snapshot")
	}
	stage.info, err = f.Stat()
	if err == nil && !private(stage.info, 0600) {
		err = errors.New("invalid staging permissions")
	}
	if err == nil {
		var n int
		n, err = f.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		removeStage(root, stage)
		return stagedFile{}, errors.New("cannot write and sync staged snapshot; history was not pruned")
	}
	return stage, nil
}

func removeStage(root *os.Root, stage stagedFile) {
	info, err := root.Lstat(stage.name)
	if err == nil && stage.info != nil && info.Mode().IsRegular() && os.SameFile(info, stage.info) {
		_ = root.Remove(stage.name)
	}
}

func unchanged(root *os.Root, name string, old snapshot) bool {
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || !private(info, 0600) || !os.SameFile(info, old.info) {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return false
	}
	data, err := localfile.Read(root, name, len(old.data))
	return err == nil && bytes.Equal(data, old.data)
}

func installPlan(root *os.Root, dir *os.File, current inventory, plan archivePlan, stage func(*os.Root, []byte) (stagedFile, error)) error {
	var names []string
	for name := range plan.writes {
		names = append(names, name)
	}
	slices.Sort(names)
	staged := map[string]stagedFile{}
	defer func() {
		for _, file := range staged {
			removeStage(root, file)
		}
	}()
	for _, name := range names {
		file, err := stage(root, plan.writes[name].data)
		if err != nil {
			return errors.New("cannot stage archive batch; history was not pruned")
		}
		staged[name] = file
	}
	// Check every old file before any replacement or retention deletion.
	for name, old := range current.files {
		if !unchanged(root, name, old) {
			return errors.New("archive changed during preparation; history was not pruned")
		}
	}
	info, err := dir.Stat()
	if err != nil || !private(info, 0700) {
		return errors.New("archive permissions changed; history was not pruned")
	}
	for _, name := range names {
		if !unchanged(root, staged[name].name, snapshot{data: plan.writes[name].data, info: staged[name].info}) {
			return errors.New("staged snapshot changed; history was not pruned")
		}
		if _, exists := current.files[name]; !exists {
			if _, err := root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
				return errors.New("archive destination changed; history was not pruned")
			}
		}
	}
	if len(names)+len(plan.remove) == 0 {
		return nil
	}
	if err := dir.Sync(); err != nil {
		return errors.New("cannot sync archive staging; history was not pruned")
	}
	for _, name := range names {
		if err := root.Rename(staged[name].name, name); err != nil {
			return errors.New("cannot install archive batch; history was not pruned")
		}
		delete(staged, name)
	}
	if err := dir.Sync(); err != nil {
		return errors.New("cannot sync refreshed history; history was not pruned")
	}
	for _, name := range plan.remove {
		if !unchanged(root, name, current.files[name]) {
			return errors.New("retention stopped because archived history changed")
		}
		if err := root.Remove(name); err != nil {
			return errors.New("cannot finish archive retention")
		}
	}
	if err := dir.Sync(); err != nil {
		return errors.New("cannot sync archive retention")
	}
	return nil
}
