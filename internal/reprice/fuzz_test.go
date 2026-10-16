// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package reprice

import "testing"

func FuzzRepriceComponents(f *testing.F) {
	f.Add(uint32(100), uint32(20), uint32(10), uint32(3), uint8(50))
	f.Add(uint32(0), uint32(0), uint32(0), uint32(0), uint8(100))
	f.Fuzz(func(t *testing.T, i, o, r, w uint32, fractionByte uint8) {
		p, fs, s := fixture()
		c := calculator(t, p, fs, s, WorkloadMap{})
		defer c.Close()
		input, output, read, write := uint64(i), uint64(o), uint64(r), uint64(w)
		record := Record{Model: "a", Input: &input, Output: &output, CacheRead: &read, CacheWrite: &write, Count: uint64(fractionByte) + 1}
		if err := c.Add(record); err != nil {
			t.Fatal(err)
		}
		result := c.Result(Source{})
		valid := read <= input && write <= input-read
		if valid != (result.Before != nil) {
			t.Fatal("cache partition misclassified")
		}
		if result.Envelope != nil {
			if rat(result.Envelope.Low).Cmp(rat(result.Envelope.High)) > 0 {
				t.Fatal("reversed interval")
			}
			if rat(*result.Level1).Sign() < 0 {
				t.Fatal("negative cost")
			}
		}
	})
}
