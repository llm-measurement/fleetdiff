// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"crypto/aes"
	"encoding/binary"
	"runtime"
	"testing"
)

// Run each subbenchmark in a fresh process with -benchmem -benchtime=1x.
// This isolates duplicate tracking, not parsing, HMAC generation, or attribution.
func BenchmarkSpendDuplicateState10M(b *testing.B) {
	const entries = 10_000_000
	for _, name := range []string{"Map32", "RequestDigests32"} {
		b.Run(name, func(b *testing.B) {
			b.StopTimer()
			block, err := aes.NewCipher(make([]byte, 16))
			if err != nil {
				b.Fatal(err)
			}
			var digest [32]byte
			var retained, capacity uint64
			b.ReportAllocs()
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				b.StartTimer()

				var seenMap map[[32]byte]struct{}
				var seen requestDigests
				if name == "Map32" {
					seenMap = map[[32]byte]struct{}{}
				}
				for i := uint64(0); i < entries; i++ {
					// AES permutes distinct counter blocks: keys are deterministic,
					// full-width and unique. Reuse scratch to avoid per-key allocations.
					clear(digest[:])
					binary.LittleEndian.PutUint64(digest[:8], i)
					binary.LittleEndian.PutUint64(digest[16:24], i)
					digest[31] = 1
					block.Encrypt(digest[:16], digest[:16])
					block.Encrypt(digest[16:], digest[16:])
					if name == "Map32" {
						if _, ok := seenMap[digest]; ok {
							b.Fatal("duplicate synthetic digest")
						}
						seenMap[digest] = struct{}{}
					} else {
						seen.add(digest)
					}
				}
				if name == "RequestDigests32" {
					if seen.Len() != entries || seen.duplicated() {
						b.Fatal("incorrect synthetic duplicate state")
					}
					for _, block := range seen {
						capacity += uint64(cap(block)) * 32
					}
				}
				b.StopTimer()

				// Keep the finished state alive across GC; obsolete growth storage
				// is excluded here, but included in B/op and external peak RSS.
				runtime.GC()
				runtime.ReadMemStats(&after)
				retained += after.HeapAlloc - before.HeapAlloc
				runtime.KeepAlive(seenMap)
				runtime.KeepAlive(seen)
			}
			b.ReportMetric(entries*32, "payload-B")
			b.ReportMetric(float64(retained)/float64(b.N), "retained-B")
			if name == "RequestDigests32" {
				b.ReportMetric(float64(capacity)/float64(b.N), "capacity-B")
			}
		})
	}
}
