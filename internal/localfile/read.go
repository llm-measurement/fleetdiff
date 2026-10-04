// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

// Package localfile provides confined reads for the two local input formats.
package localfile

import (
	"errors"
	"io"
	"os"
	"syscall"
)

// Read reads only a regular file under root. It neither follows a replaced final
// symlink nor blocks opening a substituted FIFO. Errors never include input names.
func Read(root *os.Root, name string, limit int) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("expected a regular file, not a symlink or special file")
	}
	if limit < 0 || info.Size() > int64(limit) {
		return nil, errors.New("input file or total byte limit exceeded")
	}
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errors.New("cannot open input file")
	}
	defer f.Close()
	current, err := f.Stat()
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(info, current) {
		return nil, errors.New("input file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, errors.New("cannot read input file")
	}
	if len(data) > limit {
		return nil, errors.New("input file or total byte limit exceeded")
	}
	return data, nil
}
