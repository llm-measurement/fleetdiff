// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/exportdata"
	"github.com/llm-measurement/fleetdiff/internal/langfuse"
	"github.com/llm-measurement/fleetdiff/internal/spend"
)

// These are selective safe projections of the sealed r3 capture, not native
// export byte copies. Native byte/manifest verification lives in langfuse.
func langfuseDirectory(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	paths, err := filepath.Glob("../langfuse/testdata/fresh-r3/*.jsonl")
	if err != nil || len(paths) != 2 {
		t.Fatal("safe captured fixtures missing")
	}
	for _, name := range paths {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		stem := strings.TrimSuffix(filepath.Base(name), ".jsonl")
		start, end := "2026-10-10T12:00:00Z", "2026-10-17T12:00:00Z"
		if strings.HasPrefix(stem, "2026-10-23") {
			start, end = "2026-10-17T12:00:00Z", "2026-10-23T13:19:25.482Z"
		}
		key := "synthetic-capture-project/observations_v2/" + stem + ".jsonl"
		m := map[string]any{"version": 1, "projectId": "synthetic-capture-project", "exportSource": "EVENTS", "window": map[string]string{"minTimestamp": start, "maxTimestamp": end}, "maxTimestamp": end, "files": []any{map[string]any{"key": "captures/" + key, "table": "observations_v2", "fileType": "JSONL", "format": "jsonl-raw", "compressed": false, "contentType": "application/x-ndjson; charset=utf-8", "sizeBytes": len(body), "rowCount": 14}}}
		b, _ := json.Marshal(m)
		for rel, data := range map[string][]byte{key: body, "synthetic-capture-project/manifests/" + stem + ".json": b} {
			p := filepath.Join(root, rel)
			if os.MkdirAll(filepath.Dir(p), 0700) != nil || os.WriteFile(p, data, 0600) != nil {
				t.Fatal("write CLI fixture")
			}
		}
	}
	return root
}

func TestLangfuseCLIGoldensAndPrivacy(t *testing.T) {
	root := langfuseDirectory(t)
	base := []string{"investigate", "--langfuse-export", root, "--before-period", "2026-10-10T12:00:00Z/2026-10-11T12:00:00Z", "--after-period", "2026-10-17T12:00:00Z/2026-10-18T12:00:00Z"}
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			var out, errout bytes.Buffer
			if code := Run(append(append([]string{}, base...), "--format", format), &out, &errout); code != 0 {
				t.Fatalf("code %d: %s", code, errout.String())
			}
			for _, sentinel := range []string{"synthetic-user-", "synthetic-session-", "synthetic-no-price", "FORBIDDEN_SENTINEL"} {
				if strings.Contains(out.String()+errout.String(), sentinel) {
					t.Fatal("raw private label leaked")
				}
			}
			if format == "text" && !strings.Contains(out.String(), "Recorded cost (Langfuse") {
				t.Fatal("source cost label absent")
			}
			if strings.Contains(out.String(), "LiteLLM") {
				t.Fatal("Langfuse output contains a LiteLLM source label")
			}
			if format == "json" {
				var r langfuse.Report
				if json.Unmarshal(out.Bytes(), &r) != nil || r.Schema != "fleetdiff-langfuse-spend/v1" || r.Coverage.UniqueRows != 28 || r.Before.Requests != 5 || r.After.Requests != 5 {
					t.Fatal("CLI did not compose native generation report")
				}
			}
			p := filepath.Join("testdata/langfuse", format+".golden")
			if os.Getenv("FLEETDIFF_UPDATE_LANGFUSE_GOLDEN") == "1" {
				if os.MkdirAll(filepath.Dir(p), 0700) != nil || os.WriteFile(p, out.Bytes(), 0600) != nil {
					t.Fatal("write golden")
				}
			}
			want, err := os.ReadFile(p)
			if err != nil || !bytes.Equal(want, out.Bytes()) {
				t.Fatal("CLI Langfuse golden mismatch")
			}
		})
	}
	for _, extra := range [][]string{{"--litellm-spend", "PRIVATE_PATH"}, {"--litellm-db"}, {"--litellm-daily-spend", "PRIVATE_PATH"}, {"--routing-audit"}, {"--show-labels"}, {"--period", "2026-10-10"}, {"--before", "PRIVATE_PATH"}, {"--langfuse-tag", "PRIVATE_TAG"}, {"--group-by", "metadata"}, {"--format", "xml"}} {
		var out, errout bytes.Buffer
		code := Run(append(append([]string{}, base...), extra...), &out, &errout)
		if code == 0 || out.Len() != 0 || strings.Contains(errout.String(), "PRIVATE_") {
			t.Fatal("incompatible source options accepted or leaked")
		}
	}
	var out, errout bytes.Buffer
	if Run([]string{"investigate", "--langfuse-export", "PRIVATE_MISSING_PATH", "--before-period", "2026-10-10", "--after-period", "2026-10-17"}, &out, &errout) == 0 || strings.Contains(errout.String(), "PRIVATE_") {
		t.Fatal("path leaked")
	}
}

func TestLangfusePartialCLI(t *testing.T) {
	root := langfuseDirectory(t)
	files, err := filepath.Glob(filepath.Join(root, "synthetic-capture-project/observations_v2/*"))
	if err != nil || len(files) != 2 {
		t.Fatal("partial fixture files")
	}
	if os.Remove(files[0]) != nil {
		t.Fatal("remove partial fixture")
	}
	for _, format := range []string{"text", "json"} {
		var out, errout bytes.Buffer
		code := Run([]string{"investigate", "--langfuse-export", root, "--before-period", "2026-10-10T12:00:00Z/2026-10-11T12:00:00Z", "--after-period", "2026-10-17T12:00:00Z/2026-10-18T12:00:00Z", "--format", format}, &out, &errout)
		if code != 0 {
			t.Fatal("partial CLI failed", errout.String())
		}
		if format == "text" {
			if !strings.Contains(out.String(), "All totals below are partial") || strings.Contains(out.String(), "LiteLLM") || !strings.Contains(out.String(), "restore missing or partial files") {
				t.Fatal("partial/source language missing")
			}
		} else {
			var r langfuse.Report
			if json.Unmarshal(out.Bytes(), &r) != nil || !r.Incomplete || r.Volume != nil || r.Increase != nil || r.Cache.Rankings != nil || !strings.Contains(r.TotalsScope, "partial") || r.Coverage.MissingFiles != 1 {
				t.Fatal("partial report retained whole-period claims")
			}
		}
	}
}

func TestLangfuseProviderFlagsAreMutuallyExclusive(t *testing.T) {
	for _, name := range []string{"provider-daily-counts", "provider-model-map", "provider-counts-contract"} {
		for _, value := range []string{"PRIVATE_PROVIDER_MUST_NOT_READ", ""} {
			for _, first := range []bool{false, true} {
				t.Run(name+"/"+value+"/"+strconv.FormatBool(first), func(t *testing.T) {
					root := filepath.Join(t.TempDir(), "PRIVATE_LANGFUSE_MUST_NOT_READ")
					base := []string{"--langfuse-export", root, "--before-period", "2026-10-10", "--after-period", "2026-10-17"}
					option := "--" + name + "=" + value
					args := append([]string{"investigate"}, base...)
					if first {
						args = append([]string{"investigate", option}, base...)
					} else {
						args = append(args, option)
					}
					called := false
					exporter := func(context.Context, exportdata.Options) (exportdata.Result, error) {
						called = true
						return exportdata.Result{}, nil
					}
					var out, diagnostic bytes.Buffer
					code := RunWithExporter(context.Background(), args, strings.NewReader(""), &out, &diagnostic, exporter)
					if code != 2 || out.Len() != 0 || called || !strings.Contains(diagnostic.String(), "do not combine") || !strings.Contains(diagnostic.String(), "provider counts") || strings.Contains(diagnostic.String(), "PRIVATE_") {
						t.Fatal("mixed sources reached execution or leaked input", code, diagnostic.String())
					}
				})
			}
		}
	}
}

func TestLangfuseRowsRejectProviderCoverageOptions(t *testing.T) {
	for _, opts := range []spend.Options{
		{ProviderCountsPath: "PRIVATE_PROVIDER_MUST_NOT_READ"},
		{ProviderModelMapPath: "PRIVATE_PROVIDER_MUST_NOT_READ"},
		{ProviderContractPath: "PRIVATE_PROVIDER_MUST_NOT_READ"},
	} {
		called := false
		_, err := spend.InvestigateRows(func(func(spend.Row) error) error {
			called = true
			return nil
		}, opts)
		if err == nil || called || !strings.Contains(err.Error(), "provider counts") || strings.Contains(err.Error(), "PRIVATE_") {
			t.Fatal("row source accepted provider options or leaked input")
		}
	}
}
