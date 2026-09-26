// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/llm-measurement/fleetdiff/examples/internal/scenario"
	"github.com/llm-measurement/fleetdiff/internal/compare"
	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

type operator struct{ id, name, dir, otlp, metrics string }

func runDemo(ctx context.Context, out string, progress io.Writer) (resultErr error) {
	recipe, err := scenario.Load()
	if err != nil {
		return err
	}
	out, err = filepath.Abs(out)
	if err != nil || strings.Contains(out, ",") {
		return errors.New("invalid output directory")
	}
	if err := os.Mkdir(out, 0700); err != nil {
		return errors.New("output must be a new directory with an existing parent")
	}
	imageBytes, _ := assets.ReadFile("image.txt")
	image := strings.TrimSpace(string(imageBytes))
	if _, err := docker(ctx, nil, "image", "inspect", image); err != nil {
		return errors.New("pull the image in examples/two-operators/image.txt before running")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return errors.New("cannot generate the per-run hashing key")
	}
	secret := hex.EncodeToString(key)
	verificationSecret, err := verificationKey(secret)
	if err != nil {
		return err
	}
	network := "fleetdiff-demo-" + strings.ToLower(rand.Text()[:12])
	if _, err := docker(ctx, nil, "network", "create", network); err != nil {
		return err
	}
	var operators []operator
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		for _, p := range operators {
			if resultErr != nil {
				if logs, err := docker(cleanup, nil, "logs", p.name); err == nil {
					_ = os.WriteFile(filepath.Join(out, p.id+"-collector.log"), logs, 0600)
				}
			}
			if _, err := docker(cleanup, nil, "rm", "--force", p.name); err != nil {
				resultErr = errors.Join(resultErr, errors.New("container cleanup failed; inspect containers with the fleetdiff-demo prefix"))
			}
		}
		if _, err := docker(cleanup, nil, "network", "rm", network); err != nil {
			resultErr = errors.Join(resultErr, errors.New("example network cleanup failed"))
		}
	}()
	config, _ := assets.ReadFile("collector.yaml")
	if err := os.WriteFile(filepath.Join(out, "collector.yaml"), config, 0600); err != nil {
		return errors.New("cannot write private configuration")
	}
	for _, id := range []string{"owned", "partner"} {
		p := operator{id: id, name: network + "-" + id, dir: filepath.Join(out, id)}
		if err := os.Mkdir(p.dir, 0700); err != nil {
			return errors.New("cannot create producer directory")
		}
		env := append(os.Environ(), "FLEETDIFF_DEMO_SECRET="+secret)
		args := []string{"run", "--detach", "--pull=never", "--name", p.name, "--network", network,
			"--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges:true", "--memory=512m", "--cpus=1", "--pids-limit=128",
			"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
			"--env", "FLEETDIFF_DEMO_SECRET", "--env", "FLEETDIFF_DEMO_PRODUCER=" + id,
			"--env", "FLEETDIFF_DEMO_KEY_ID=" + network,
			"--publish", "127.0.0.1::4318", "--publish", "127.0.0.1::8889", "--publish", "127.0.0.1::13133",
			"--mount", "type=bind,src=" + filepath.Join(out, "collector.yaml") + ",dst=/config.yaml,readonly",
			"--mount", "type=bind,src=" + p.dir + ",dst=/summaries", image, "--config", "/config.yaml"}
		// Register cleanup before Docker can create a container, including interrupted starts.
		operators = append(operators, p)
		if _, err := docker(ctx, env, args...); err != nil {
			return err
		}
		endpoint := func(port string) (string, error) {
			data, err := docker(ctx, nil, "port", p.name, port+"/tcp")
			if err != nil {
				return "", err
			}
			address := strings.TrimSpace(string(data))
			host, _, err := net.SplitHostPort(address)
			if err != nil || host != "127.0.0.1" {
				return "", errors.New("collector endpoint is not loopback")
			}
			return "http://" + address, nil
		}
		p.otlp, err = endpoint("4318")
		if err != nil {
			return err
		}
		p.metrics, err = endpoint("8889")
		if err != nil {
			return err
		}
		health, err := endpoint("13133")
		if err != nil {
			return err
		}
		for {
			if _, err := request(ctx, http.MethodGet, health, nil); err == nil {
				break
			}
			if err := waitUntil(ctx, time.Now().Add(250*time.Millisecond)); err != nil {
				return err
			}
		}
		operators[len(operators)-1] = p
	}
	fmt.Fprintln(progress, "Two isolated collectors are ready. Waiting for complete observation windows.")
	start := nextWindow(time.Now())
	for i, window := range []string{"before", "after"} {
		boundary := start.Add(time.Duration(i) * windowDuration)
		if err := waitUntil(ctx, boundary.Add(500*time.Millisecond)); err != nil {
			return err
		}
		for _, p := range operators {
			if time.Now().After(boundary.Add(windowDuration - 4*time.Second)) {
				return errors.New("missed the fixture send window; rerun in a new directory")
			}
			data, _ := assets.ReadFile("fixtures/" + window + "-" + p.id + ".json")
			if err := send(ctx, p.otlp, data); err != nil {
				return err
			}
		}
		fmt.Fprintln(progress, "Sent the", window, "fixture once to each collector.")
	}
	if err := waitUntil(ctx, start.Add(2*windowDuration+2*time.Second)); err != nil {
		return err
	}
	for _, window := range []string{"before", "after"} {
		if err := os.MkdirAll(filepath.Join(out, "handoff", window), 0700); err != nil {
			return errors.New("cannot create private handoff")
		}
	}
	for _, p := range operators {
		metrics, err := request(ctx, http.MethodGet, p.metrics+"/metrics", nil)
		if err != nil {
			return err
		}
		if !bytes.Contains(metrics, []byte("gen_ai_sketch_requests_total")) || !bytes.Contains(metrics, []byte(`overflow="true"`)) {
			return errors.New("metric and label-overflow surfaces were not exercised")
		}
		if err := noPrivateContent(metrics); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(out, p.id+"-metrics.txt"), metrics, 0600); err != nil {
			return errors.New("cannot save metrics")
		}
		if _, err := docker(ctx, nil, "stop", "--time", "5", p.name); err != nil {
			return err
		}
		logs, err := docker(ctx, nil, "logs", p.name)
		if err != nil {
			return err
		}
		if !bytes.Contains(logs, []byte("genaisketch topk snapshot")) {
			return errors.New("no top-k log snapshot inside the privacy scan")
		}
		if err := noPrivateContent(logs); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(out, p.id+"-collector.log"), logs, 0600); err != nil {
			return errors.New("cannot save collector logs")
		}
		for i, window := range []string{"before", "after"} {
			selected := start.Add(time.Duration(i) * windowDuration).UnixNano()
			docs, err := compare.ReadWindow(p.dir, &selected)
			if err != nil || len(docs) != 1 {
				return errors.New("expected one actual collector snapshot per producer/window")
			}
			doc := docs[0]
			var requests, roots uint64
			for _, call := range recipe.Windows[i].Calls {
				if call.Producer == p.id {
					requests++
					if call.Agent == "supervisor" {
						roots++
					}
				}
			}
			if doc.ObservedStart != selected || doc.ObservedEnd != selected+windowDuration.Nanoseconds() || doc.Counters["requests"] != requests || doc.Counters["agent_runs"] != roots {
				return errors.New("collector window is incomplete or fixture accounting changed")
			}
			// Copy the original file unchanged; never invent coverage or rewrite timestamps.
			data, err := os.ReadFile(filepath.Join(p.dir, fmt.Sprintf("%020d-%s.json", selected, doc.Epoch)))
			if err != nil || len(data) > summary.MaxBytes {
				return errors.New("cannot read completed summary")
			}
			if err := noPrivateContent(data); err != nil {
				return err
			}
			for _, payload := range doc.Sketches {
				if err := noPrivateContent(payload.Data); err != nil {
					return err
				}
			}
			if err := os.WriteFile(filepath.Join(out, "handoff", window, p.id+".json"), data, 0600); err != nil {
				return errors.New("cannot write handoff snapshot")
			}
		}
		// Logs and metrics must not expose pseudonymous item hashes as metric values or labels.
		if err := checkMetricHashes(p.dir, metrics); err != nil {
			return err
		}
	}
	if err := checkHandoff(out, image, start, verificationSecret); err != nil {
		return err
	}
	fmt.Fprintln(progress, "PASS: accounting, cross-operator merge, coverage, replay, compatibility, and privacy checks.")
	fmt.Fprintln(progress, "Reports and the summary-only handoff are in the new private output directory.")
	return nil
}

func verificationKey(value string) (sketchhash.Secret, error) {
	const env = "FLEETDIFF_DEMO_VERIFY_SECRET"
	previous, present := os.LookupEnv(env)
	defer func() {
		if present {
			_ = os.Setenv(env, previous)
		} else {
			_ = os.Unsetenv(env)
		}
	}()
	if err := os.Setenv(env, value); err != nil {
		return sketchhash.Secret{}, err
	}
	return sketchhash.SecretFromEnv(env)
}

func noPrivateContent(data []byte) error {
	for _, forbidden := range []string{"FC_PRIVATE_", "web_search", "fetch_page", `"timeout"`, `"not_found"`, `"tool_error"`} {
		if bytes.Contains(data, []byte(forbidden)) {
			return errors.New("raw fixture content leaked to an output surface")
		}
	}
	return nil
}
