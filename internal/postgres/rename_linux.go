//go:build linux && (amd64 || arm64)

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
	// Linux renameat2, RENAME_NOREPLACE. Unsupported kernels/filesystems
	// fail closed; ordinary Rename would overwrite an existing directory.
	_, _, errno := syscall.Syscall6(renameat2Trap, from.Fd(), uintptr(unsafe.Pointer(a)), to.Fd(), uintptr(unsafe.Pointer(b)), 1, 0)
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
	_, _, errno := syscall.Syscall6(syscall.SYS_UNLINKAT, parent.Fd(), uintptr(unsafe.Pointer(p)), 0x200, 0, 0, 0)
	runtime.KeepAlive(parent)
	if errno != 0 {
		return errno
	}
	return nil
}
