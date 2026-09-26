// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package compare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/frequentitems"
	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/hllpp"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestMalformedResourceUsage(t *testing.T) {
	binary := os.Getenv("FLEETDIFF_RESOURCE_BINARY")
	if binary == "" {
		t.Skip("set FLEETDIFF_RESOURCE_BINARY")
	}
	e := fixture(t, minute, fixtureSource{Producer: "team", Users: []uint64{1}, Prompts: []uint64{2}, Weights: []int64{3}})
	d, err := protoregistry.GlobalFiles.FindDescriptorByName("llm.sketchkit.v1.Sketch")
	if err != nil {
		t.Fatal(err)
	}
	m := dynamicpb.NewMessage(d.(protoreflect.MessageDescriptor))
	p := e.Sketches["top_prompts"]
	if err := proto.Unmarshal(p.Data, m); err != nil {
		t.Fatal(err)
	}
	body := m.Get(m.Descriptor().Fields().ByName("frequent_items")).Message()
	entries := body.Mutable(body.Descriptor().Fields().ByName("entries")).List()
	entry := entries.Get(0)
	entries.Truncate(0)
	for range 200_000 {
		entries.Append(entry)
	}
	p.Data, err = proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	e.Sketches["top_prompts"] = p
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > summary.MaxBytes {
		t.Fatal("malformed case must fit the input byte limit")
	}
	path := filepath.Join(t.TempDir(), "malformed.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "compare", "--before", path, "--after", path, "--expected", "team")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	err = cmd.Run()
	elapsed := time.Since(start)
	if err == nil || ctx.Err() != nil || cmd.ProcessState.ExitCode() != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "invalid or noncanonical summary") {
		t.Fatalf("malformed state was not rejected: %v %s", err, stderr.String())
	}
	rss := cmd.ProcessState.SysUsage().(*syscall.Rusage).Maxrss
	if runtime.GOOS == "linux" {
		rss *= 1024
	}
	t.Logf("malformed_entries=200000 input_bytes=%d elapsed_ms=%d peak_rss_bytes=%d exit=1 report_bytes=0", len(data), elapsed.Milliseconds(), rss)
}

// Opt-in measurements run the built CLI in fresh processes; fixture construction
// and the Go test runner are excluded from reported child RSS and elapsed time.
func TestResourceUsage(t *testing.T) {
	binary := os.Getenv("FLEETDIFF_RESOURCE_BINARY")
	if binary == "" {
		t.Skip("set FLEETDIFF_RESOURCE_BINARY to an absolute fleetdiff binary path")
	}
	h, err := hllpp.New("default", sketchhash.UserV1, sketchhash.HMACSHA25664)
	if err != nil {
		t.Fatal(err)
	}
	f, err := frequentitems.New("default", sketchhash.PromptV1, sketchhash.HMACSHA25664)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(1); i <= 200_000; i++ {
		h.AddHash(i * 0x9e3779b97f4a7c15)
	}
	for i := uint64(1); i <= 1024; i++ {
		if err := f.AddHash(i, 1000); err != nil {
			t.Fatal(err)
		}
	}
	hb, err := h.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	fb, err := f.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"512-small-files", "near-limit-dense-hll", "near-limit-full-fi"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			base := fixture(t, minute, fixtureSource{Producer: "p000"})
			base.Sketches = map[string]summary.Payload{}
			if name != "512-small-files" {
				p := summary.Payload{Kind: "hllpp", Data: hb}
				if name == "near-limit-full-fi" {
					p = summary.Payload{Kind: "frequent_items", Data: fb}
				}
				for i := range 16 {
					base.Sketches[fmt.Sprintf("measurement_%02d", i)] = p
				}
			}
			encoded, err := base.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			files := min(MaxFiles, MaxInputBytes/(len(encoded)+16))
			producers := make([]string, min(files, 128))
			for i := range producers {
				producers[i] = fmt.Sprintf("p%03d", i)
			}
			var sideBytes int
			for side, start := range map[string]int64{"before": minute, "after": 2 * minute} {
				path := filepath.Join(dir, side)
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				total := 0
				for i := range files {
					e := base
					e.ProducerID = producers[i%len(producers)]
					e.Sequence = uint64(i/len(producers) + 1)
					e.WindowStart, e.ObservedStart = start, start
					e.ObservedEnd, e.EmittedAt = start+minute, start+minute
					data, err := e.MarshalBinary()
					if err != nil {
						t.Fatal(err)
					}
					total += len(data)
					if err := os.WriteFile(filepath.Join(path, fmt.Sprintf("%03d.json", i)), data, 0600); err != nil {
						t.Fatal(err)
					}
				}
				if total > MaxInputBytes {
					t.Fatal("invalid resource fixture")
				}
				sideBytes = max(sideBytes, total)
			}
			for run := 1; run <= 3; run++ {
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				command := exec.CommandContext(ctx, binary, "compare", "--before", filepath.Join(dir, "before"), "--after", filepath.Join(dir, "after"), "--expected", strings.Join(producers, ","), "--format", "json")
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = &stdout, &stderr
				start := time.Now()
				err := command.Run()
				elapsed := time.Since(start)
				cancel()
				if err != nil {
					t.Fatalf("resource run failed: %v %s", err, stderr.String())
				}
				var report Report
				if err := json.Unmarshal(stdout.Bytes(), &report); err != nil || !report.Complete {
					t.Fatal("invalid resource report")
				}
				rss := command.ProcessState.SysUsage().(*syscall.Rusage).Maxrss
				if runtime.GOOS == "linux" {
					rss *= 1024
				}
				t.Logf("run=%d files_per_side=%d bytes_per_side=%d producers=%d sketches_per_file=%d elapsed_ms=%d peak_rss_bytes=%d", run, files, sideBytes, len(producers), len(base.Sketches), elapsed.Milliseconds(), rss)
			}
		})
	}
}
