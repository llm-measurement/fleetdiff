// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package inspect

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/canon"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/frequentitems"
	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/hllpp"
	commonpb "go.opentelemetry.io/proto/slim/otlp/common/v1"
)

// Small HLL++ is p=14. This nominal statistical RSE is not a hard error bound.
const nominalRSE = 1.04 / 128

type identityState struct {
	field                 string
	domain                sketchhash.Domain
	sources               []string
	resource              bool
	present, tokenCovered uint64
	cleanTokens           uint64
	distinct              *hllpp.Sketch
	attempts, tokens      *frequentitems.Sketch
}

type dimensionState struct {
	name         string
	hidden       bool
	observations uint64
	sketch       *hllpp.Sketch
}

type analyzer struct {
	secret            []byte
	result            Report
	identities        []*identityState
	dimensions        map[[32]byte]*dimensionState
	unknownDimensions int
	showNames         bool
	knownOrigin       uint64
	cleanUsage        uint64
}

// Inspect reads a bounded local capture and returns only counts, safe field
// names, and aliases (or explicitly requested names/hashes). Errors never quote input.
func Inspect(path string, stdin io.Reader, opts Options) (Report, error) {
	if opts.Top < 1 || opts.Top > 100 {
		return Report{}, errors.New("top must be between 1 and 100")
	}
	if opts.InputFormat == "" {
		opts.InputFormat = "auto"
	}
	if !slices.Contains([]string{"auto", "json", "proto", "file-proto"}, opts.InputFormat) {
		return Report{}, errors.New("unsupported input format")
	}
	a, err := newAnalyzer(opts.SecretEnv)
	if err != nil {
		return Report{}, err
	}
	defer clear(a.secret)
	a.showNames = opts.ShowNames
	err = readInputs(path, stdin, func(data []byte, ext string) error {
		a.result.Capture.Files++
		a.result.Capture.Bytes += len(data)
		format := opts.InputFormat
		if format == "auto" {
			if ext == ".pb" || ext == ".bin" {
				return errors.New("binary capture requires --input-format proto or file-proto")
			}
			format = "json"
		}
		return visitRecords(data, format, a.consume)
	})
	if err != nil {
		return Report{}, err
	}
	return a.report(opts), nil
}

func newAnalyzer(env string) (*analyzer, error) {
	secret := make([]byte, 32)
	mode := "ephemeral"
	if env == "" {
		if _, err := rand.Read(secret); err != nil {
			return nil, errors.New("cannot generate inspection hash secret")
		}
	} else {
		if len(env) > 128 || len(os.Getenv(env)) > 4096 {
			return nil, errors.New("invalid inspection secret environment setting")
		}
		if _, err := sketchhash.SecretFromEnv(env); err != nil {
			return nil, errors.New("inspection secret is missing or weak; use a random secret of at least 16 bytes")
		}
		secret = []byte(os.Getenv(env))
		mode = "environment"
	}
	a := &analyzer{secret: secret, dimensions: map[[32]byte]*dimensionState{}, result: Report{
		Schema: "fleetdiff-inspect/v1", AccountingID: AccountingID, Hashing: mode, Metrics: map[string]uint64{}, TokenObservations: map[string]uint64{}, UsageProvenance: map[string]uint64{}, OperationMix: map[string]uint64{}, Dimensions: []Dimension{}, Identities: []Identity{}, NextSteps: []string{},
	}}
	for _, name := range []string{"requests_total", "agent_runs_total", "input_tokens_total", "output_tokens_total", "total_tokens_total", "cache_read_input_tokens_total", "cache_write_input_tokens_total", "reasoning_output_tokens_total", "missing_token_usage_total", "dedup_suppressed_total", "dedup_key_missing_total"} {
		a.result.Metrics[metricPrefix+name] = 0
	}
	for _, f := range tokenFields {
		for _, state := range []string{"reported", "missing", "invalid", "conflict", "subset_violation"} {
			a.result.TokenObservations[f.Name+"/"+state] = 0
		}
	}
	for _, f := range []string{"input", "output"} {
		for _, source := range []string{"provider_reported", "inferred", "unavailable", "unknown"} {
			a.result.UsageProvenance[f+"/"+source] = 0
		}
	}
	a.identities = []*identityState{
		{field: "user", domain: sketchhash.UserV1, sources: []string{"enduser.id", "user.id"}, resource: true},
		{field: "session", domain: sketchhash.SessionV1, sources: []string{"gen_ai.conversation.id", "session.id"}},
		{field: "prompt", domain: sketchhash.PromptV1, sources: []string{"gen_ai.request.prompt"}, resource: true},
	}
	for _, id := range a.identities {
		id.distinct, _ = hllpp.New(hllpp.ProfileSmall, id.domain, sketchhash.HMACSHA25664)
		id.attempts, _ = frequentitems.New(frequentitems.ProfileSmall, id.domain, sketchhash.HMACSHA25664)
		id.tokens, _ = frequentitems.New(frequentitems.ProfileSmall, id.domain, sketchhash.HMACSHA25664)
	}
	return a, nil
}

func (a *analyzer) consume(req *traceRequest) error {
	a.result.Capture.Records++
	if a.result.Capture.Records > MaxRecords {
		return errors.New("capture exceeds 10000 records")
	}
	for _, rs := range req.ResourceSpans {
		resource, err := attributes(rs.GetResource().GetAttributes())
		if err != nil {
			return err
		}
		a.result.Capture.DroppedAttributes += uint64(rs.GetResource().GetDroppedAttributesCount())
		for _, ss := range rs.GetScopeSpans() {
			for _, span := range ss.GetSpans() {
				a.result.Capture.Spans++
				if a.result.Capture.Spans > MaxSpans {
					return errors.New("capture exceeds 100000 spans")
				}
				a.result.Capture.DroppedAttributes += uint64(span.GetDroppedAttributesCount())
				attrs, err := attributes(span.GetAttributes())
				if err != nil {
					return err
				}
				if !validID(span.GetTraceId(), 16) || !validID(span.GetSpanId(), 8) || !validID(span.GetParentSpanId(), 8) {
					return errEncoding
				}
				op := stringAttribute(attrs, "gen_ai.operation.name")
				if op == "invoke_agent" && allZero(span.GetParentSpanId()) {
					a.result.Metrics[metricPrefix+"agent_runs_total"]++
				}
				if !modelAttempt(attrs) {
					category := "other"
					switch op {
					case "invoke_agent":
						category = "agent"
					case "execute_tool":
						category = "tool"
					case "retrieval":
						category = "retrieval"
					case "invoke_workflow":
						category = "workflow"
					}
					a.result.OperationMix[category]++
					continue
				}
				a.result.OperationMix["model"]++
				counts, err := accountTokens(attrs)
				if err != nil {
					return err
				}
				if err = mergeCounts(a.result.Metrics, counts.Metrics); err != nil {
					return err
				}
				if err = mergeCounts(a.result.TokenObservations, counts.Quality); err != nil {
					return err
				}
				if err = mergeCounts(a.result.UsageProvenance, counts.Provenance); err != nil {
					return err
				}
				if counts.Provenance["input/unknown"] == 0 && counts.Provenance["output/unknown"] == 0 {
					a.knownOrigin++
				}
				clean := counts.Metrics[metricPrefix+"missing_token_usage_total"] == 0
				for _, f := range tokenFields {
					for _, issue := range []string{"invalid", "conflict", "subset_violation"} {
						if counts.Quality[f.Name+"/"+issue] > 0 {
							clean = false
						}
					}
				}
				if clean {
					a.cleanUsage++
				}
				for _, id := range a.identities {
					v, present := firstValue(attrs, id.sources)
					if !present && id.resource {
						v, _ = firstValue(resource, id.sources)
					}
					text := attributeString(v)
					if text == "" {
						continue
					}
					if len(text) > 8192 {
						return errors.New("identity attribute exceeds 8 KiB")
					}
					canonical, err := canon.CanonicalizeString(canon.TextV1, text)
					if err != nil {
						return errors.New("invalid identity attribute")
					}
					h := binary.BigEndian.Uint64(a.digest(string(id.domain), canonical)[:8])
					id.distinct.AddHash(h)
					id.present++
					if err = id.attempts.AddHash(h, 1); err != nil {
						return errOverflow
					}
					if counts.Metrics[metricPrefix+"missing_token_usage_total"] == 0 {
						id.tokenCovered++
						if clean {
							id.cleanTokens++
						}
						weight := counts.Metrics[metricPrefix+"total_tokens_total"]
						if weight > 0 {
							if err = id.tokens.AddHash(h, int64(weight)); err != nil {
								return errOverflow
							}
						}
					}
				}
				for _, k := range slices.Sorted(maps.Keys(resource)) {
					v := resource[k]
					if _, overridden := attrs[k]; !overridden {
						if err = a.observeDimension(k, v); err != nil {
							return err
						}
					}
				}
				for _, k := range slices.Sorted(maps.Keys(attrs)) {
					v := attrs[k]
					if err = a.observeDimension(k, v); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func (a *analyzer) digest(domain string, b []byte) []byte {
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(domain))
	mac.Write([]byte{0})
	mac.Write(b)
	return mac.Sum(nil)
}

func (a *analyzer) observeDimension(key string, v *commonpb.AnyValue) error {
	for _, field := range tokenFields {
		if slices.Contains(field.Sources, key) {
			return nil
		}
	}
	var id [32]byte
	copy(id[:], a.digest("inspect-attribute-name", []byte(key)))
	d := a.dimensions[id]
	if d == nil {
		if len(a.dimensions) >= MaxDimensions {
			return errors.New("capture exceeds 128 attribute dimensions")
		}
		name := key
		hidden := !a.showNames && !publicAttribute(key)
		if hidden {
			a.unknownDimensions++
			name = fmt.Sprintf("attribute-%d", a.unknownDimensions)
		}
		s, _ := hllpp.New(hllpp.ProfileSmall, sketchhash.UserV1, sketchhash.HMACSHA25664)
		d = &dimensionState{name: name, hidden: hidden, sketch: s}
		a.dimensions[id] = d
	}
	// Metric labels use AsString, not protobuf's typed representation. Do not
	// apply identity canonicalization: label whitespace/case is significant.
	h := a.digest("inspect-attribute-value", []byte(attributeString(v)))
	d.sketch.AddHash(binary.BigEndian.Uint64(h[:8]))
	d.observations++
	return nil
}

func firstValue(attrs map[string]*commonpb.AnyValue, keys []string) (*commonpb.AnyValue, bool) {
	for _, key := range keys {
		if v, ok := attrs[key]; ok {
			return v, true
		}
	}
	return nil, false
}
func validID(b []byte, n int) bool { return len(b) == 0 || len(b) == n }
func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}
