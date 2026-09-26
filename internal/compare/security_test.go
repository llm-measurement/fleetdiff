// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package compare

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"
)

func rewriteFIWeight(t testing.TB, data []byte, total int64) []byte {
	t.Helper()
	d, err := protoregistry.GlobalFiles.FindDescriptorByName("llm.sketchkit.v1.Sketch")
	if err != nil {
		t.Fatal(err)
	}
	m := dynamicpb.NewMessage(d.(protoreflect.MessageDescriptor))
	if err := proto.Unmarshal(data, m); err != nil {
		t.Fatal(err)
	}
	body := m.Get(m.Descriptor().Fields().ByName("frequent_items")).Message()
	body.Set(body.Descriptor().Fields().ByName("total_weight"), protoreflect.ValueOfInt64(total))
	encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func FuzzEmbeddedSketch(f *testing.F) {
	base := fixture(f, minute, fixtureSource{Producer: "team", Users: []uint64{1}, Prompts: []uint64{2}, Weights: []int64{3}})
	f.Add(false, base.Sketches["distinct_users"].Data)
	f.Add(true, base.Sketches["top_prompts"].Data)
	f.Add(true, []byte("SENTINEL_PAYLOAD"))
	f.Fuzz(func(t *testing.T, weighted bool, data []byte) {
		if len(data) > 4<<20 {
			return
		}
		kind := "hllpp"
		if weighted {
			kind = "frequent_items"
		}
		a, b := base, base
		a.Sketches = map[string]summary.Payload{"unreported_measurement": {Kind: kind, Data: data}}
		b.Sketches = a.Sketches
		b.WindowStart += minute
		b.ObservedStart += minute
		b.ObservedEnd += minute
		b.EmittedAt += minute
		r, err := Compare([]summary.Envelope{a}, []summary.Envelope{b}, Options{Expected: []string{"team"}, Top: 20})
		if err != nil && (!reflect.DeepEqual(r, Report{}) || strings.Contains(err.Error(), "SENTINEL")) {
			t.Fatal("invalid payload exposed a report or input content")
		}
	})
}

func TestRejectsInconsistentFrequentItemTotals(t *testing.T) {
	for _, total := range []int64{0, 6, 20} {
		for _, name := range []string{"top_prompts", "unreported_measurement"} {
			a := fixture(t, minute, fixtureSource{Producer: "team", Users: []uint64{1, 2}, Prompts: []uint64{1, 2}, Weights: []int64{5, 5}})
			b := fixture(t, 2*minute, fixtureSource{Producer: "team", Users: []uint64{1, 2}, Prompts: []uint64{1, 2}, Weights: []int64{5, 5}})
			payload := a.Sketches["top_prompts"]
			payload.Data = rewriteFIWeight(t, payload.Data, total)
			a.Sketches[name] = payload
			b.Sketches[name] = b.Sketches["top_prompts"]
			r, err := Compare([]summary.Envelope{a}, []summary.Envelope{b}, Options{Expected: []string{"team"}, Top: 20})
			if err == nil || !reflect.DeepEqual(r, Report{}) {
				t.Fatalf("inconsistent total %d accepted for %s", total, name)
			}
		}
	}
}

func TestSupersededStateStillValidated(t *testing.T) {
	a := fixture(t, minute, fixtureSource{Producer: "team", Users: []uint64{1}, Prompts: []uint64{2}, Weights: []int64{3}})
	bad := a
	bad.Sketches = map[string]summary.Payload{}
	for name, p := range a.Sketches {
		bad.Sketches[name] = p
	}
	p := bad.Sketches["top_prompts"]
	p.Data = rewriteFIWeight(t, p.Data, 0)
	bad.Sketches["top_prompts"] = p
	a.Sequence = 2
	b := fixture(t, 2*minute, fixtureSource{Producer: "team", Users: []uint64{1}, Prompts: []uint64{2}, Weights: []int64{3}})
	r, err := Compare([]summary.Envelope{bad, a}, []summary.Envelope{b}, Options{Expected: []string{"team"}, Top: 20})
	if err == nil || !reflect.DeepEqual(r, Report{}) {
		t.Fatal("superseded invalid state was accepted")
	}
}

func TestReadRegularReplacementRace(t *testing.T) {
	dir := t.TempDir()
	path, staged := filepath.Join(dir, "input.json"), filepath.Join(dir, "replacement")
	outside := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(outside, []byte("SENTINEL_OUTSIDE"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("safe"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	stop, done := make(chan struct{}), make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				done <- nil
				return
			default:
			}
			if err := os.Symlink(outside, staged); err != nil {
				done <- err
				return
			}
			if err := os.Rename(staged, path); err != nil {
				done <- err
				return
			}
			if err := os.WriteFile(staged, []byte("safe"), 0600); err != nil {
				done <- err
				return
			}
			if err := os.Rename(staged, path); err != nil {
				done <- err
				return
			}
		}
	}()
	t.Cleanup(func() {
		close(stop)
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	for range 300 {
		data, err := readRegular(root, "input.json", 64)
		if err == nil && string(data) != "safe" {
			t.Fatal("read escaped through replacement symlink")
		}
	}
}
