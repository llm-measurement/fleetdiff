//go:build !unix

package langfuse

import "testing"

func makeFIFO(t *testing.T, _ string) { t.Skip("FIFO confinement test requires Unix") }
