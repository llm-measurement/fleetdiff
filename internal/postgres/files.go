// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package postgres

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/llm-measurement/fleetdiff/internal/exportdata"
)

var errOutput = exportdata.ErrOutput

func owned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

// Walk from an open root, checking each component and its opened identity.
func openParent(path string) (*os.Root, string, error) {
	return walkParent(path, nil)
}

func openOutputParent(path string) (*os.Root, string, error) {
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if part == ".." {
			return nil, "", errOutput
		}
	}
	return walkParent(path, trustedOutputParent)
}

func trustedOutputParent(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.IsDir() && (stat.Uid == 0 || stat.Uid == uint32(os.Geteuid())) &&
		(info.Mode().Perm()&0022 == 0 || info.Mode()&os.ModeSticky != 0)
}

func walkParent(path string, trusted func(os.FileInfo) bool) (*os.Root, string, error) {
	if path == "" {
		return nil, "", errOutput
	}
	abs, err := filepath.Abs(path)
	if err != nil || abs == "/" {
		return nil, "", errOutput
	}
	root, err := os.OpenRoot("/")
	if err != nil {
		return nil, "", errOutput
	}
	if trusted != nil {
		info, err := root.Stat(".")
		if err != nil || !trusted(info) {
			root.Close()
			return nil, "", errOutput
		}
	}
	parts := strings.Split(strings.TrimPrefix(abs, "/"), "/")
	for _, part := range parts[:len(parts)-1] {
		info, e := root.Lstat(part)
		if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			root.Close()
			return nil, "", errOutput
		}
		next, e := root.OpenRoot(part)
		if e != nil {
			root.Close()
			return nil, "", errOutput
		}
		opened, e := next.Stat(".")
		root.Close()
		if e != nil || !os.SameFile(info, opened) || trusted != nil && !trusted(opened) {
			next.Close()
			return nil, "", errOutput
		}
		root = next
	}
	return root, parts[len(parts)-1], nil
}

type outputDir struct {
	parent, container, root             *os.Root
	parentFile, containerFile, rootFile *os.File
	path, name, stage                   string
	stageInfo, parentInfo               os.FileInfo
	created                             map[string]os.FileInfo
	complete                            bool
}

func newOutput(path string) (*outputDir, error) {
	parent, name, err := openOutputParent(path)
	if err != nil {
		return nil, err
	}
	if _, err = parent.Lstat(name); !os.IsNotExist(err) {
		parent.Close()
		return nil, errOutput
	}
	d := &outputDir{parent: parent, path: path, name: name, created: make(map[string]os.FileInfo)}
	ok := false
	defer func() {
		if !ok {
			d.close()
		}
	}()
	d.parentInfo, err = parent.Stat(".")
	if err != nil {
		return nil, errOutput
	}
	d.parentFile, err = parent.Open(".")
	if err != nil {
		return nil, errOutput
	}
	for attempts := 0; attempts < 32; attempts++ {
		d.stage = ".fleetdiff-stage-" + rand.Text()
		if err = parent.Mkdir(d.stage, 0700); !os.IsExist(err) {
			break
		}
	}
	if err != nil {
		d.stage = ""
		return nil, errOutput
	}
	d.stageInfo, err = parent.Lstat(d.stage)
	if err != nil || !d.stageInfo.IsDir() || !owned(d.stageInfo) || d.stageInfo.Mode().Perm() != 0700 {
		return nil, errOutput
	}
	d.container, err = parent.OpenRoot(d.stage)
	if err != nil {
		return nil, errOutput
	}
	opened, err := d.container.Stat(".")
	if err != nil || !os.SameFile(d.stageInfo, opened) {
		return nil, errOutput
	}
	d.containerFile, err = d.container.Open(".")
	if err != nil {
		return nil, errOutput
	}
	// The public sibling name is never the rename source. An attacker may
	// replace that name, but cannot redirect operations on this private root.
	if err = d.container.Mkdir("export", 0700); err != nil {
		return nil, errOutput
	}
	d.root, err = d.container.OpenRoot("export")
	if err != nil {
		return nil, errOutput
	}
	d.rootFile, err = d.root.Open(".")
	if err != nil {
		return nil, errOutput
	}
	ok = true
	return d, nil
}

func (d *outputDir) close() {
	if d.parent == nil {
		return
	}
	if !d.complete && d.root != nil {
		for name := range d.created {
			_ = d.root.Remove(name)
		}
	}
	if d.rootFile != nil {
		d.rootFile.Close()
	}
	if d.root != nil {
		d.root.Close()
	}
	if !d.complete && d.containerFile != nil {
		_ = removeEmptyDirectoryAt(d.containerFile, "export")
	}
	if d.containerFile != nil {
		d.containerFile.Close()
	}
	if d.container != nil {
		d.container.Close()
	}
	// Only rmdir is safe through this mutable public name. A last-moment
	// replacement can at most lose an empty directory, never files or a tree.
	if d.stageInfo != nil && d.parentFile != nil {
		if now, err := d.parent.Lstat(d.stage); err == nil && os.SameFile(d.stageInfo, now) {
			_ = removeEmptyDirectoryAt(d.parentFile, d.stage)
		}
	}
	if d.parentFile != nil {
		d.parentFile.Close()
	}
	d.parent.Close()
	d.parent = nil
}

func (d *outputDir) create(name string) (*os.File, error) {
	if d.complete || d.parent == nil || name != "spend.csv" && name != "spend.jsonl" && name != "daily.csv" {
		return nil, errOutput
	}
	f, err := d.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, errOutput
	}
	d.created[name] = nil
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !owned(info) || info.Mode().Perm() != 0600 {
		f.Close()
		return nil, errOutput
	}
	d.created[name] = info
	return f, nil
}

// publish has one commit point: a successful no-replace directory rename.
// Cancellation after that point must not roll back or report an absent export.
func (d *outputDir) publish(ctx context.Context) error {
	if d.parent == nil || d.complete || len(d.created) == 0 {
		return errOutput
	}
	_, csv := d.created["spend.csv"]
	_, jsonl := d.created["spend.jsonl"]
	if csv == jsonl {
		return errOutput
	}
	// Detect a caller-path rebind before committing. The syscall still targets
	// the held parent if an external actor renames an ancestor after this check.
	parent, name, err := openOutputParent(d.path)
	if err != nil {
		return errOutput
	}
	current, err := parent.Stat(".")
	parent.Close()
	if err != nil || name != d.name || !os.SameFile(d.parentInfo, current) {
		return errOutput
	}
	source, err := d.container.Lstat("export")
	held, heldErr := d.root.Stat(".")
	if err != nil || heldErr != nil || !source.IsDir() || !os.SameFile(source, held) {
		return errOutput
	}
	entries, err := d.rootFile.ReadDir(-1)
	if err != nil || len(entries) != len(d.created) {
		return errOutput
	}
	for _, entry := range entries {
		original, ok := d.created[entry.Name()]
		current, err := d.root.Lstat(entry.Name())
		if !ok || original == nil || err != nil || !os.SameFile(original, current) || !current.Mode().IsRegular() || current.Mode().Perm() != 0600 {
			return errOutput
		}
	}
	if err := d.rootFile.Sync(); err != nil {
		return errOutput
	}
	if ctx.Err() != nil {
		return exportdata.ErrInterrupted
	}
	if err := renameDirectoryNoReplace(d.containerFile, "export", d.parentFile, d.name); err != nil {
		return errOutput
	}
	d.complete = true
	return nil
}
