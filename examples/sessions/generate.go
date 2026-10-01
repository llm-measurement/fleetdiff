// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

// Generate reproducible synthetic summaries, not captured provider traffic.
package main

import (
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/llm-measurement/fleetdiff/examples/internal/scenario"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/frequentitems"
	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/hllpp"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

func main() {
	out := flag.String("out", "", "new summary directory (must not exist)")
	flag.Parse()
	if *out == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "provide -out with a new output directory")
		os.Exit(2)
	}
	if err := generate(*out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func generate(output string) error {
	// Public fixture key for reproducibility; never use it for real identities.
	if err := os.Setenv("FLEETDIFF_SESSIONS_FIXTURE_KEY", "public-sessions-fixture-key-not-for-production"); err != nil {
		return err
	}
	secret, err := sketchhash.SecretFromEnv("FLEETDIFF_SESSIONS_FIXTURE_KEY")
	if err != nil {
		return err
	}
	if err := os.Mkdir(output, 0700); err != nil {
		return err
	}
	for side, name := range []string{"before", "after"} {
		doc, err := sessionWindow(side, secret)
		if err != nil {
			return err
		}
		data, err := doc.MarshalBinary()
		if err != nil {
			return err
		}
		dir := filepath.Join(output, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "app.json"), data, 0600); err != nil {
			return err
		}
	}
	return nil
}

func sessionWindow(side int, secret sketchhash.Secret) (summary.Envelope, error) {
	const duration = int64(15 * time.Second)
	start := int64(side+1) * duration
	doc := summary.Envelope{
		Version: 1, Sequence: 1, ProducerID: "app", Epoch: "synthetic-sessions",
		ScopeID: "sessions-demo", KeyID: "public-synthetic-key", AccountingID: "synthetic-sessions-v1",
		WindowStart: start, WindowDuration: duration, ObservedStart: start,
		ObservedEnd: start + duration, EmittedAt: start + duration,
		Counters: map[string]uint64{"requests": 4, "input_tokens": 320, "output_tokens": 80, "missing_token_usage": 0},
		Sketches: map[string]summary.Payload{},
	}
	if side == 1 {
		doc.Counters["requests"], doc.Counters["input_tokens"], doc.Counters["output_tokens"] = 13, 2740, 560
	}
	for _, field := range []struct {
		name   string
		domain sketchhash.Domain
	}{
		{"prompts", sketchhash.PromptV1}, {"users", sketchhash.UserV1}, {"sessions", sketchhash.SessionV1},
	} {
		top, err := frequentitems.New("small", field.domain, sketchhash.HMACSHA25664)
		if err != nil {
			return summary.Envelope{}, err
		}
		distinct, err := hllpp.New("small", field.domain, sketchhash.HMACSHA25664)
		if err != nil {
			return summary.Envelope{}, err
		}
		for i := 0; i < int(doc.Counters["requests"]); i++ {
			key, weight := fmt.Sprintf("SESSIONS_PRIVATE_%s_%d", field.name, i), int64(100)
			if side == 1 {
				if field.name != "users" {
					key += "_after"
				}
				if i >= 3 {
					key, weight = "SESSIONS_PRIVATE_"+field.name+"_repeated", 300
				}
			}
			h, err := scenario.Hash(secret, field.domain, key)
			if err != nil {
				return summary.Envelope{}, err
			}
			distinct.AddHash(h)
			if err := top.AddHash(h, weight); err != nil {
				return summary.Envelope{}, err
			}
		}
		data, err := top.MarshalBinary()
		if err != nil {
			return summary.Envelope{}, err
		}
		doc.Sketches["top_"+field.name] = summary.Payload{Kind: "frequent_items", Data: data}
		if field.name != "prompts" {
			// This is a synthetic recipe contract, not a collector extraction contract.
			digest := sha256.Sum256([]byte("synthetic-sessions-v1:" + field.name + ":tokens"))
			doc.Counters[fmt.Sprintf("topk_contract.v1.top_%s.%x", field.name, digest[:16])] = 0
		}
		if field.name != "sessions" {
			data, err := distinct.MarshalBinary()
			if err != nil {
				return summary.Envelope{}, err
			}
			doc.Sketches["distinct_"+field.name] = summary.Payload{Kind: "hllpp", Data: data}
		}
	}
	return doc, nil
}
