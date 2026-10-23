//go:build unix

package langfuse

import (
	"syscall"
	"testing"
)

func makeFIFO(t *testing.T, name string) {
	t.Helper()
	if syscall.Mkfifo(name, 0600) != nil {
		t.Fatal("create FIFO fixture")
	}
}
