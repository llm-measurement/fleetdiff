# Inspect Your Traces

`fleetdiff inspect` answers "will my data work?" before summary export is set up.
It reads a local capture, checks seven questions, and gives a next action. This
command is included in v0.4.0. Use the verified binary or build from source:

```sh
go build -o bin/fleetdiff ./cmd/fleetdiff
bin/fleetdiff inspect traces.jsonl
bin/fleetdiff inspect --format json captures/
bin/fleetdiff inspect --input-format proto - < traces.pb
```

Put flags before the input. [Three capture recipes](../examples/inspect/README.md)
cover the Collector file exporter, Python SDK, and LiteLLM. `sh examples/inspect.sh`
runs a synthetic example without Docker or provider credentials.

## Questions And Readiness

| Question | What inspect checks |
|---|---|
| Can I count model activity? | Model operations or the default model-name fallback |
| Can I measure token usage? | Both parent usage fields, with no invalid, conflicting, or subset-violating observations |
| Do I know where the usage came from? | Input and output provenance declarations, including unavailable usage |
| Can I count distinct users? | A supported user key on model attempts or their resources |
| Can I rank users by tokens? | A user key and complete, consistent usage on the same attempts |
| Can I rank sessions? | A session key on model spans; attempt weighting works even without tokens |
| Can I rank prompt signatures? | A supported prompt field, under your existing content-collection policy |

`ready` means every observed model attempt meets that question's field requirements;
`partial` means some do; `cannot_determine` means none do. The headline counts only
fully ready questions. Readiness describes the retained capture, not unsampled
traffic, delivery completeness, or correctness of an instrumentation assertion.
Declared dropped attributes are counted separately.

Stock LiteLLM can report zeros or estimates for absent provider usage. Without a
provenance declaration, inspect cannot distinguish a true zero from a filled-in
one. The callback recipe records source information before normalization for its
documented supported paths. Streaming origin remains unknown.

## Accounting

The fixed default contract is `genai-default-accounting/v1+usage-provenance/v1`.
The [accounting corpus](../testdata/inspect-contract/v1/manifest.json) pins its collector
source revision and LiteLLM fixture origin. Both repositories execute these same
inputs and expected counters in CI. It covers operation scope, locality, aliases,
missing and conflicting usage, subsets, provenance, retries, and overflow.

- Count `chat`, `generate_content`, `text_completion`, and `embeddings` as model
  attempts. With no usable span operation, a nonempty span `gen_ai.request.model`
  is the fallback. Resource-only operations do not turn a span into a model attempt.
- A root `invoke_agent` span increments agent runs. Agent, tool, retrieval, and
  workflow wrappers do not inflate model attempts. Unknown operations stay outside
  the model count. Scope attributes are not inherited.
- Use current input/output usage names with legacy prompt/completion aliases.
  First valid source wins; invalid and conflicting sources remain reported as
  quality observations. Token attributes come from the model span only.
- A model attempt is missing usage if either parent token field is unavailable.
  Explicit `unavailable` suppresses placeholder values. Independently observed
  input or output still contributes to its own counter.
- Cache-read/write are subsets of input; reasoning is a subset of output.
  They are never added to total tokens again. A reported subset larger than its
  reported parent is flagged; its original count is retained.
- Deduplication is off. Every retained attempt counts, including retry attempts.
  Do not combine overlapping captures or repeat the same file. Inspect does not
  create event-time or processing-time windows, or infer rates from capture duration.

Custom connector mappings, filtering, deduplication, and MCP side counters are
outside this default inspection contract. Configure the collector separately for
your production policy. Inspection does not emit a collector configuration.

## Identities And Cardinality

| Field | First-present source order | Resource fallback |
|---|---|---|
| User | `enduser.id`, `user.id` | Yes |
| Session | `gen_ai.conversation.id`, `session.id` | No |
| Prompt | `gen_ai.request.prompt` | Yes |

Span values take precedence, including an empty first-present value. Identity
values use the collector's scalar/string conversion, `text_v1` canonicalization,
and domain-separated HMAC-SHA256-64. Empty values are absent. Nothing tries to
reconstruct a prompt or invent a session from trace IDs.

Each identity has an HLL++ distinct estimate and two frequent-item rankings:
attempts and tokens. Token rankings use only attempts with both usage fields;
zero-token attempts add no token weight. A missing key is not attributed to a
default identity. Shares use **attributed** weight, not the whole capture's weight.
Aliases refer to the same retained identity across both weightings in one report.
`max_error`, candidate count, and the display limit describe the retained head,
not an exact ranking of every identity. Bounds describe observed weights, not
proof of billing accuracy or a runaway loop.

Per-field cardinality examines effective resource plus span attributes on model
attempts, with span values overriding the same resource key. It uses the collector's
string conversion, without identity canonicalization: `"1"`, integer `1`, and double
`1` are one label value, while whitespace and case remain distinct. Unknown names become
`attribute-N`; known standard names are allowlisted. No values are printed.
Usage token counts and their legacy aliases are excluded from label review;
they remain in the accounting and quality checks. Other numeric attributes are
still reviewed.

When the leading field is aliased, the next action names that alias and its
estimated cardinality. Rerun locally with `--show-names` to find the source field:

```sh
bin/fleetdiff inspect --show-names traces.jsonl
```

Exact rankings print as `session-1: 8 (20.00%)`. Unequal bounds stay ranges;
percentage ranges round outward. JSON retains integer bounds and stable question
IDs such as `model_activity`; text uses readable names such as "Model activity".

An identifier's label risk is explicit even when it has only one value. Other
dimensions with at least about 1,000 estimated values are marked high-cardinality;
smaller ones say review, never safe. This estimate is **not a Prometheus series
forecast**. Other labels, metrics, and observed combinations determine total series.

The small HLL++ profile has p=14, with nominal relative standard error
`1.04 / sqrt(2^14)` (about 0.81%). This is a statistical error scale, not a hard
interval. Frequent-item bounds are deterministic for retained observed weight.

## Privacy

The CLI makes no network requests, opens no receiver, and writes no files. It
holds decoded raw input transiently in memory to compute the report. A fresh
cryptographic key is created for each run; aggregate state retains hashes and
counts rather than raw values. Garbage collection is not a secure memory eraser.

Default output hides raw values, span/event names, file paths, arbitrary attribute
names, and hashes. It exposes counts, known attribute names, and report-local
aliases. Review aggregate activity before sharing under your data policy.

`--show-names` exposes custom attribute **names**, never their values. Names can
themselves contain sensitive information, so leave this off for shared reports.
Text output quotes names and escapes control and non-ASCII characters; JSON
retains the original name as a JSON string. `name_hidden: true` marks aliases.

`--secret-env NAME` loads a non-placeholder random secret of at least 16 bytes
(maximum 4 KiB). Do not put the key on the command line. Reusing a key allows
cross-run matching only when `--show-hashes` is also used. Those hashes are
pseudonymous and linkable, not anonymous. Do not share raw captures in issues.

## Formats

- JSON: OTLP `ExportTraceServiceRequest`, using hexadecimal trace/span IDs.
- JSON lines: uncompressed Collector `file` exporter output, one request per line.
  Pretty-printed and whitespace-separated requests are also accepted.
- `--input-format proto`: one raw binary OTLP trace export request.
- `--input-format file-proto`: Collector framing, a four-byte big-endian length
  followed by each protobuf request.

`auto` reads JSON. Binary framing must be selected explicitly. A directory is
nonrecursive and selects `.json`, `.jsonl`, `.otlp`, `.pb`, and `.bin`, sorted by
filename; other entries are ignored. Every selected file must parse. Put only
captures in that directory. A selected symlink or special file is an error.
Compressed exports, SDK diagnostic console JSON, and arbitrary vendor exports
need a documented encoder or a separate adapter; they are not accepted as OTLP.

Duplicate JSON members and duplicate attribute keys are rejected. Unknown OTLP
protocol fields are ignored for forward compatibility, not promoted to report
fields. An empty OTLP request gives zero ready questions; an empty file is an error.

## Limits

| Input limit | Value |
|---|---|
| Total encoded input | 32 MiB |
| One decoded OTLP record | 8 MiB encoded |
| Selected files / directory entries | 512 / 1,024 |
| Records / spans | 10,000 / 100,000 |
| Distinct effective attribute names | 128 |
| Attributes per map / elements per array | 256 |
| Attribute key / encoded value | 256 bytes / 64 KiB |
| Converted identity value | 8 KiB |
| JSON / attribute nesting | 32 / 16 levels |
| JSON nodes / protobuf message objects per record | 250,000 / 250,000 |
| Resource or scope groups per parent | 1,024 |
| Span events or links per span | 256 |
| Displayed candidates per ranking | 1-100, default 10 |

Per-span token values/sums and sketch totals must fit signed 64-bit integers.
Cumulative counters reject unsigned 64-bit overflow. Protobuf message counts are
checked from the wire before allocating decoded objects. Reaching a limit fails the
whole inspection, without a partial-success report. These bounds do not promise
a fixed peak RSS or runtime; use OS process limits for untrusted captures.

## JSON And Exit Codes

JSON schema `fleetdiff-inspect/v1` is separate from comparison JSON v1. Consumers
should ignore new object members and tolerate additional question IDs. Changing
existing field meaning requires a new schema or accounting identifier.

`observed_counters` retains exact unsigned capture totals. `metrics` uses those
same names and the collector's signed-64-bit saturation behavior for exported
samples; `metric_saturations` names any clipped values. Text uses exact observed
totals. `token_observations` and `usage_provenance` use the collector contract's
fixed names; readiness, dimensions, and identities are separate sections.
Integers can exceed JavaScript's exact numeric range: use a lossless JSON parser
when consuming large counts. Exit 0 means a report, including partial or zero
readiness; exit 1 means input/accounting/output failure; exit 2 means bad options.
Validation finishes before output begins. A failed stdout write can leave a prefix;
never consume output from a failed run.

## Verify

```sh
go test -race ./...
go vet ./...
go test ./internal/inspect -run '^$' -fuzz '^FuzzTraceRecord$' -fuzztime 30s
```

Both repos retain the versioned corpus and checksum manifest; accounting changes
must update the contract deliberately. To compare local copies explicitly:

```sh
INSPECT_CONTRACT_COUNTERPART="$PWD/../otelcol-genai-sketches/connector/genaisketchconnector/testdata/inspect-contract/v1" \
  go test ./internal/inspect -run '^TestInspectContractCounterpart$'
```

Use an absolute counterpart path when running tests: Go runs each test package
from its source directory. The [capture recipe tests](../examples/inspect/README.md#verify-the-recipes)
also run actual SDK encoders, the pinned file exporter, and LiteLLM against a local
synthetic provider. No paid calls are part of these checks.
