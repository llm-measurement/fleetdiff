//go:build darwin && (amd64 || arm64)

// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package postgres

import (
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

func renameDirectoryNoReplace(from *os.File, old string, to *os.File, name string) error {
	a, err := syscall.BytePtrFromString(old)
	if err != nil {
		return err
	}
	b, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	// Darwin SYS_renameatx_np, RENAME_EXCL; the Go amd64 wrapper adds
	// the syscall class itself. Never fall back to an overwriting rename.
	_, _, errno := syscall.Syscall6(488, from.Fd(), uintptr(unsafe.Pointer(a)), to.Fd(), uintptr(unsafe.Pointer(b)), 4, 0)
	runtime.KeepAlive(from)
	runtime.KeepAlive(to)
	if errno != 0 {
		return errno
	}
	return nil
}

func removeEmptyDirectoryAt(parent *os.File, name string) error {
	p, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	// Darwin SYS_unlinkat, AT_REMOVEDIR: never unlink a replacement file
	// or symlink, and never remove a nonempty replacement directory.
	_, _, errno := syscall.Syscall6(472, parent.Fd(), uintptr(unsafe.Pointer(p)), 0x80, 0, 0, 0)
	runtime.KeepAlive(parent)
	if errno != 0 {
		return errno
	}
	return nil
}
