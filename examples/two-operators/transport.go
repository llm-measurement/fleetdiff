// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"time"
)

var errOutputLimit = errors.New("example output exceeded its limit")

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errOutputLimit
	}
	return b.Buffer.Write(p)
}

func docker(ctx context.Context, env []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = env
	stdout, stderr := &limitedBuffer{limit: 2 << 20}, &limitedBuffer{limit: 2 << 20}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("Docker %s failed; check the daemon and pinned image, or inspect the private run directory", args[0])
	}
	if len(args) > 0 && args[0] == "logs" {
		if _, err := stdout.Write(stderr.Bytes()); err != nil {
			return nil, err
		}
	}
	return stdout.Bytes(), nil
}

// No proxy or redirect can forward fixture content beyond the explicit loopback endpoint.
var localHTTP = &http.Client{
	Timeout:       3 * time.Second,
	Transport:     &http.Transport{Proxy: nil},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func request(ctx context.Context, method, url string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("invalid local endpoint")
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := localHTTP.Do(req)
	if err != nil {
		return nil, errors.New("local collector request failed")
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 || res.StatusCode != http.StatusOK {
		return nil, errors.New("collector returned an unsuccessful or oversized response")
	}
	return data, nil
}

func send(ctx context.Context, endpoint string, data []byte) error {
	// Retrying an ambiguous OTLP write could double-count; fail this run instead.
	body, err := request(ctx, http.MethodPost, endpoint+"/v1/traces", data)
	if err != nil {
		return err
	}
	var response struct {
		Partial *struct {
			Rejected json.RawMessage `json:"rejectedSpans"`
			Message  string          `json:"errorMessage"`
		} `json:"partialSuccess"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return errors.New("invalid OTLP response")
	}
	if response.Partial != nil {
		r := string(response.Partial.Rejected)
		if response.Partial.Message != "" || (r != "" && r != `"0"` && r != "0") {
			return errors.New("OTLP partial success; no retry was attempted")
		}
	}
	return nil
}

func waitUntil(ctx context.Context, when time.Time) error {
	timer := time.NewTimer(time.Until(when))
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return errors.New("example timed out or was interrupted")
	}
}

func nextWindow(now time.Time) time.Time { return now.Truncate(windowDuration).Add(windowDuration) }

func writeJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
