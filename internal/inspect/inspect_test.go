// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package inspect

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/canon"
	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
	commonpb "go.opentelemetry.io/proto/slim/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/slim/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/slim/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

func inspectRequest(t *testing.T, req *traceRequest, opts Options) Report {
	t.Helper()
	data, err := proto.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	opts.InputFormat = "proto"
	if opts.Top == 0 {
		opts.Top = 10
	}
	r, err := Inspect("-", bytes.NewReader(data), opts)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func captureRequest(t *testing.T) *traceRequest {
	t.Helper()
	r, err := decodeJSON([]byte(minimalCapture))
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func attr(key, value string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: textValue(value)}
}

func TestIdentityPrivacyAndHashContract(t *testing.T) {
	t.Setenv("INSPECT_TEST_KEY", "a-long-random-looking-test-key-91304728164")
	req := captureRequest(t)
	span := req.ResourceSpans[0].ScopeSpans[0].Spans[0]
	span.Name = "SENTINEL_span_name"
	span.Attributes = append(span.Attributes, attr("enduser.id", " SENTINEL_user "), attr("session.id", "SENTINEL_session"), attr("gen_ai.request.prompt", "SENTINEL_prompt"), attr("SENTINEL_attribute_name", "SENTINEL_value"), attr("mcp.resource.uri", "SENTINEL_uri"))
	span.Events = []*tracepb.Span_Event{{Name: "SENTINEL_event"}}
	for _, hashes := range []bool{false, true} {
		r := inspectRequest(t, req, Options{ShowHashes: hashes, SecretEnv: "INSPECT_TEST_KEY"})
		data, _ := json.Marshal(r)
		if bytes.Contains(data, []byte("SENTINEL")) || bytes.Contains(data, []byte(os.Getenv("INSPECT_TEST_KEY"))) {
			t.Fatal("raw value or secret leaked")
		}
		for _, id := range r.Identities {
			if id.Present != 1 || id.TokenCovered != 1 || math.Abs(id.Distinct-1) > .01 {
				t.Fatal("identity was not measured")
			}
			for _, rank := range id.Rankings {
				if len(rank.Items) != 1 || (rank.Items[0].Hash != "") != hashes {
					t.Fatal("hash output policy changed")
				}
			}
		}
		if hashes {
			secret, _ := sketchhash.SecretFromEnv("INSPECT_TEST_KEY")
			c, _ := canon.CanonicalizeString(canon.TextV1, " SENTINEL_user ")
			h, _ := sketchhash.Hash64(secret, sketchhash.UserV1, c)
			if r.Identities[0].Rankings[0].Items[0].Hash != fmt.Sprintf("%016x", h) {
				t.Fatal("hash differs from sketchkit")
			}
		}
	}
	a := inspectRequest(t, req, Options{ShowHashes: true})
	b := inspectRequest(t, req, Options{ShowHashes: true})
	if a.Identities[0].Rankings[0].Items[0].Hash == b.Identities[0].Rankings[0].Items[0].Hash {
		t.Fatal("per-run secret reused")
	}
	a = inspectRequest(t, req, Options{ShowHashes: true, SecretEnv: "INSPECT_TEST_KEY"})
	b = inspectRequest(t, req, Options{ShowHashes: true, SecretEnv: "INSPECT_TEST_KEY"})
	if !reflect.DeepEqual(a, b) {
		t.Fatal("stable key did not produce stable report")
	}
}

func TestIdentityLocalityAndFirstPresent(t *testing.T) {
	req := captureRequest(t)
	rs := req.ResourceSpans[0]
	span := rs.ScopeSpans[0].Spans[0]
	rs.Resource = &resourcepb.Resource{Attributes: []*commonpb.KeyValue{attr("enduser.id", "resource-user"), attr("gen_ai.request.prompt", "resource-prompt"), attr("session.id", "resource-session")}}
	r := inspectRequest(t, req, Options{})
	if r.Identities[0].Present != 1 || r.Identities[2].Present != 1 || r.Identities[1].Present != 0 {
		t.Fatal("default resource fallback changed")
	}
	span.Attributes = append(span.Attributes, attr("enduser.id", ""), attr("user.id", "fallback-must-not-win"), attr("session.id", "span-session"))
	r = inspectRequest(t, req, Options{})
	if r.Identities[0].Present != 0 || r.Identities[1].Present != 1 {
		t.Fatal("first-present semantics changed")
	}
}

func TestCustomNamesAreOptInAndUsageIsNotALabel(t *testing.T) {
	t.Setenv("INSPECT_TEST_KEY", "test-only-stable-fixture-key-81934624610")
	req := captureRequest(t)
	base := req.ResourceSpans[0].ScopeSpans[0].Spans[0]
	spans := make([]*tracepb.Span, 40)
	for i := range spans {
		s := proto.Clone(base).(*tracepb.Span)
		s.Attributes = append(s.Attributes, attr("acme.customer_email", fmt.Sprintf("SENTINEL_customer_%d", i)), attr("acme.segment", "SENTINEL_segment"))
		spans[i] = s
	}
	req.ResourceSpans[0].ScopeSpans[0].Spans = spans
	hidden := inspectRequest(t, req, Options{SecretEnv: "INSPECT_TEST_KEY"})
	shown := inspectRequest(t, req, Options{SecretEnv: "INSPECT_TEST_KEY", ShowNames: true})
	if !reflect.DeepEqual(hidden.ObservedCounters, shown.ObservedCounters) || !reflect.DeepEqual(hidden.Identities, shown.Identities) || !reflect.DeepEqual(hidden.Readiness, shown.Readiness) {
		t.Fatal("name display changed measurements")
	}
	for _, r := range []Report{hidden, shown} {
		data, _ := json.Marshal(r)
		if bytes.Contains(data, []byte("SENTINEL")) {
			t.Fatal("attribute values leaked")
		}
		for _, d := range r.Dimensions {
			for _, field := range tokenFields {
				for _, source := range field.sources {
					if d.Attribute == source {
						t.Fatal("numeric usage field listed as label candidate")
					}
				}
			}
		}
	}
	if d := hidden.Dimensions[0]; !d.NameHidden || d.Attribute != "attribute-1" || math.Abs(d.Estimate-40) > 1 || !strings.Contains(hidden.NextSteps[0], "attribute-1 has ~40 distinct values; rerun with --show-names") {
		t.Fatal("missing actionable hidden-name guidance", hidden.NextSteps)
	}
	if d := shown.Dimensions[0]; d.NameHidden || d.Attribute != "acme.customer_email" {
		t.Fatal("requested name was not revealed")
	}
	encoded, _ := json.Marshal(hidden)
	if bytes.Contains(encoded, []byte("acme.")) {
		t.Fatal("custom name leaked by default")
	}
}

func TestEveryTokenSourceIsExcludedFromLabelReview(t *testing.T) {
	a, err := newAnalyzer("")
	if err != nil {
		t.Fatal(err)
	}
	defer clear(a.secret)
	for _, field := range tokenFields {
		for _, source := range field.sources {
			if err := a.observeDimension(source, integer(40)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(a.dimensions) != 0 {
		t.Fatal("usage sources consumed label review state")
	}
	if err := a.observeDimension("custom.numeric_dimension", integer(40)); err != nil || len(a.dimensions) != 1 {
		t.Fatal("unrelated numeric dimensions should still be reviewed", err)
	}
}

func TestMissingUsageStillRanksAttempts(t *testing.T) {
	req := captureRequest(t)
	span := req.ResourceSpans[0].ScopeSpans[0].Spans[0]
	span.Attributes = []*commonpb.KeyValue{attr("gen_ai.operation.name", "chat"), attr("session.id", "session")}
	r := inspectRequest(t, req, Options{})
	id := r.Identities[1]
	if id.TokenCovered != 0 || id.Rankings[0].Total != 1 || id.Rankings[1].Total != 0 {
		t.Fatal("missing usage invented token weight or lost attempts")
	}
	if r.UsageProvenance["input/unknown"] != 1 {
		t.Fatal("missing origin treated as known")
	}
}

func TestAliasesAreSharedAcrossWeights(t *testing.T) {
	req := captureRequest(t)
	s := req.ResourceSpans[0].ScopeSpans[0].Spans[0]
	s.Attributes = append(s.Attributes, attr("enduser.id", "one"))
	s2 := proto.Clone(s).(*tracepb.Span)
	s2.Attributes = append(s2.Attributes[:len(s2.Attributes)-1], attr("enduser.id", "two"))
	s2.Attributes[1].Value = integer(100)
	req.ResourceSpans[0].ScopeSpans[0].Spans = append([]*tracepb.Span{s}, s2, s)
	r := inspectRequest(t, req, Options{ShowHashes: true})
	aliases := map[string]string{}
	for _, ranking := range r.Identities[0].Rankings {
		for _, item := range ranking.Items {
			if old := aliases[item.Hash]; old != "" && old != item.Alias {
				t.Fatal("identity alias differs across weights")
			}
			aliases[item.Hash] = item.Alias
		}
	}
}

func TestCaptureLimitsAndPrivateErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*traceRequest)
	}{
		{"duplicate key", func(r *traceRequest) {
			s := r.ResourceSpans[0].ScopeSpans[0].Spans[0]
			s.Attributes = append(s.Attributes, s.Attributes[0])
		}},
		{"long key", func(r *traceRequest) {
			r.ResourceSpans[0].ScopeSpans[0].Spans[0].Attributes = append(r.ResourceSpans[0].ScopeSpans[0].Spans[0].Attributes, attr(strings.Repeat("SENTINEL", 40), "x"))
		}},
		{"oversize identity", func(r *traceRequest) {
			r.ResourceSpans[0].ScopeSpans[0].Spans[0].Attributes = append(r.ResourceSpans[0].ScopeSpans[0].Spans[0].Attributes, attr("enduser.id", strings.Repeat("SENTINEL", 2000)))
		}},
		{"dimensions", func(r *traceRequest) {
			s := r.ResourceSpans[0].ScopeSpans[0].Spans[0]
			for i := 0; i < 129; i++ {
				s.Attributes = append(s.Attributes, attr(fmt.Sprintf("SENTINEL_%d", i), "x"))
			}
		}},
		{"spans", func(r *traceRequest) {
			s := r.ResourceSpans[0].ScopeSpans[0].Spans[0]
			s.Attributes = nil
			r.ResourceSpans[0].ScopeSpans[0].Spans = make([]*tracepb.Span, MaxSpans+1)
			for i := range r.ResourceSpans[0].ScopeSpans[0].Spans {
				r.ResourceSpans[0].ScopeSpans[0].Spans[i] = s
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := captureRequest(t)
			tc.mutate(r)
			b, _ := proto.Marshal(r)
			got, err := Inspect("-", bytes.NewReader(b), Options{InputFormat: "proto", Top: 10})
			if err == nil || strings.Contains(err.Error(), "SENTINEL") || got.Schema != "" {
				t.Fatalf("acceptance, leak, or partial report: %v", err)
			}
		})
	}
	for _, b := range [][]byte{bytes.Repeat([]byte(" "), MaxInputBytes+1), bytes.Repeat([]byte(`{"resourceSpans":[]}`), MaxRecords+1)} {
		if _, err := Inspect("-", bytes.NewReader(b), Options{InputFormat: "json", Top: 10}); err == nil {
			t.Fatal("oversized capture accepted")
		}
	}
	frame := binary.BigEndian.AppendUint32(nil, 0)
	if _, err := Inspect("-", bytes.NewReader(bytes.Repeat(frame, MaxRecords+1)), Options{InputFormat: "file-proto", Top: 10}); err == nil {
		t.Fatal("too many frames accepted")
	}
}

func TestCaptureFilesAndDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.jsonl")
	if err := os.WriteFile(path, []byte(minimalCapture), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.json"), []byte(minimalCapture), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("SENTINEL"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := Inspect(dir, nil, Options{Top: 10})
	if err != nil || r.Metrics[metricPrefix+"requests_total"] != 2 || r.Capture.Files != 2 {
		t.Fatalf("directory: %v", err)
	}
	link := filepath.Join(t.TempDir(), "SENTINEL.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{link, filepath.Dir(link), filepath.Join(dir, "SENTINEL_missing")} {
		if _, err := Inspect(p, nil, Options{Top: 10}); err == nil || strings.Contains(err.Error(), "SENTINEL") {
			t.Fatalf("unsafe path result: %v", err)
		}
	}
	fifo := filepath.Join(t.TempDir(), "pipe.json")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(fifo, nil, Options{Top: 10}); err == nil {
		t.Fatal("FIFO accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "c.json"), []byte(`{"SENTINEL":`), 0600); err != nil {
		t.Fatal(err)
	}
	if r, err := Inspect(dir, nil, Options{Top: 10}); err == nil || r.Schema != "" {
		t.Fatal("late failure returned partial report")
	}
}

func TestEmptyCaptureAndQualityReadiness(t *testing.T) {
	r, err := Inspect("-", strings.NewReader(`{"resourceSpans":[]}`), Options{Top: 10})
	if err != nil || r.Readiness.Ready != 0 {
		t.Fatal("empty capture is ready", err)
	}
	req := captureRequest(t)
	req.ResourceSpans[0].ScopeSpans[0].Spans[0].Attributes = append(req.ResourceSpans[0].ScopeSpans[0].Spans[0].Attributes, &commonpb.KeyValue{Key: "gen_ai.usage.prompt_tokens", Value: integer(999)})
	r = inspectRequest(t, req, Options{})
	for _, q := range r.Readiness.Questions {
		if q.ID == "token_usage" && q.Status == "ready" {
			t.Fatal("conflicting usage reported ready")
		}
	}
}

func TestWirePreflightPreventsRepeatedMessageAllocation(t *testing.T) {
	wrap := func(field protowire.Number, b []byte) []byte {
		return protowire.AppendBytes(protowire.AppendTag(nil, field, protowire.BytesType), b)
	}
	spans := bytes.Repeat([]byte{0x12, 0}, MaxSpans+1)
	wire := wrap(1, wrap(2, spans))
	if preflightProto(wire) == nil {
		t.Fatal("empty span expansion accepted")
	}
	allocations := testing.AllocsPerRun(2, func() {
		if err := preflightProto(wire); err == nil {
			panic("preflight did not reject")
		}
	})
	if allocations > 1000 {
		t.Fatalf("preflight allocated per span: %.0f allocations", allocations)
	}
	called := false
	if err := visitProto(wire, func(*traceRequest) error { called = true; return nil }); err == nil || called {
		t.Fatal("oversized structure reached decoder consumer")
	}
}

func TestLabelConversionAndNominalPrecision(t *testing.T) {
	req := captureRequest(t)
	s := req.ResourceSpans[0].ScopeSpans[0].Spans[0]
	var spans []*tracepb.Span
	for _, value := range []*commonpb.AnyValue{textValue("1"), integer(1), {Value: &commonpb.AnyValue_DoubleValue{DoubleValue: 1}}} {
		x := proto.Clone(s).(*tracepb.Span)
		x.Attributes = append(x.Attributes, &commonpb.KeyValue{Key: "team.id", Value: value})
		spans = append(spans, x)
	}
	req.ResourceSpans[0].ScopeSpans[0].Spans = spans
	r := inspectRequest(t, req, Options{})
	for _, d := range r.Dimensions {
		if d.Attribute == "team.id" && (math.Abs(d.Estimate-1) > .01 || d.Observations != 3) {
			t.Fatal("label values should use collector string conversion")
		}
	}
	a, err := newAnalyzer("")
	if err != nil {
		t.Fatal(err)
	}
	defer clear(a.secret)
	if a.identities[0].distinct.DenseRegisterBytes() != 1<<14 {
		t.Fatal("small-profile precision changed; review nominal RSE")
	}
}

func TestNontrivialRankingBounds(t *testing.T) {
	t.Setenv("INSPECT_TEST_KEY", "test-only-stable-fixture-key-81934624610")
	req := captureRequest(t)
	s := req.ResourceSpans[0].ScopeSpans[0].Spans[0]
	truth := map[string]int64{}
	secret, _ := sketchhash.SecretFromEnv("INSPECT_TEST_KEY")
	spans := make([]*tracepb.Span, 1400)
	for i := range spans {
		name := fmt.Sprintf("key-%d", i)
		weight := int64(1 + i%7)
		if i < 2 {
			weight = 10000
		}
		x := proto.Clone(s).(*tracepb.Span)
		x.Attributes[1].Value = integer(weight)
		x.Attributes[2].Value = integer(0)
		x.Attributes = append(x.Attributes, attr("enduser.id", name))
		spans[i] = x
		canonical, _ := canon.CanonicalizeString(canon.TextV1, name)
		h, _ := sketchhash.Hash64(secret, sketchhash.UserV1, canonical)
		truth[fmt.Sprintf("%016x", h)] = weight
	}
	req.ResourceSpans[0].ScopeSpans[0].Spans = spans
	r := inspectRequest(t, req, Options{Top: 100, SecretEnv: "INSPECT_TEST_KEY", ShowHashes: true})
	ranking := r.Identities[0].Rankings[1]
	if ranking.MaxError == 0 {
		t.Fatal("test did not exercise approximation")
	}
	for _, item := range ranking.Items {
		if n := truth[item.Hash]; n < item.Lower || n > item.Upper {
			t.Fatalf("bounds exclude truth: %+v actual %d", item, n)
		}
	}
}
