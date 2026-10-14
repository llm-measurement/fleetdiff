// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"bytes"
	"sort"
)

const digestBlock = 1 << 15

// Fixed blocks avoid retaining both old and new large backing arrays when an
// export grows. sort.Interface sorts them in place without a contiguous copy.
type requestDigests [][][32]byte

func (d *requestDigests) add(h [32]byte) {
	if len(*d) == 0 || len((*d)[len(*d)-1]) == digestBlock {
		*d = append(*d, make([][32]byte, 0, digestBlock))
	}
	i := len(*d) - 1
	(*d)[i] = append((*d)[i], h)
}

func (d requestDigests) Len() int {
	if len(d) == 0 {
		return 0
	}
	return (len(d)-1)*digestBlock + len(d[len(d)-1])
}

func (d requestDigests) at(i int) *[32]byte { return &d[i/digestBlock][i%digestBlock] }
func (d requestDigests) Less(i, j int) bool { return bytes.Compare(d.at(i)[:], d.at(j)[:]) < 0 }
func (d requestDigests) Swap(i, j int)      { a, b := d.at(i), d.at(j); *a, *b = *b, *a }

func (d requestDigests) duplicated() bool {
	sort.Sort(d)
	for i := 1; i < d.Len(); i++ {
		if *d.at(i) == *d.at(i - 1) {
			return true
		}
	}
	return false
}
