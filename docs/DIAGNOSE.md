# Diagnose Collector Configuration

Included in fleetdiff v0.5.0. Use the [verified release binary](OPERATIONS.md#install-and-verify)
or build with `go build -o bin/fleetdiff ./cmd/fleetdiff`.

`fleetdiff diagnose` is a local, static check for supported collector YAML. It
does not run the collector, load extensions, inspect environment values, read
include files, open network connections, or modify the input. There is no upload.

```sh
fleetdiff diagnose collector.yaml
fleetdiff diagnose --format json collector.yaml
fleetdiff diagnose --format json - < collector.yaml
```

Flags precede the single file or `-`. JSON version 1 uses fixed status names,
allowlisted finding IDs and severity, YAML key paths, line/column numbers, and
known attribute names. Text groups findings at the same setting and line:

```text
2 problems to fix:
  connectors.genaisketch.operation_filter.llm_operations (line 10): Keep only model operations (chat, generate_content, text_completion, embeddings), each once. Tool and agent operations count as model attempts if included.
  connectors.genaisketch.slices[0].keys (line 12): enduser.id is a metric label; remove it from slices and use a hashed field instead.
```

Safe configurations print `Looks safe: no blocking findings.` A path uses known
schema keys and zero-based sequence positions. Custom mapping keys use one-based
`[key-N]` positions so even a secret embedded in a component name stays hidden.
JSON retains each underlying check and its numeric node reference, plus the
static-check limits. A reference is a one-based preorder YAML node ordinal;
zero means the whole document or an absent node. No arbitrary source names,
filenames, values, line excerpts, environment contents, or parser errors enter
reports. Locations and known schema names disclose configuration structure.

## Status Codes

| Code | Meaning |
| --- | --- |
| 0 | `supported_safe`: supported static checks passed, or help was displayed. |
| 1 | Input read/size/type failure or output failure. |
| 2 | Invalid options. |
| 3 | `unsafe`: blocking risks in supported mappings. |
| 4 | `indeterminate`: unsupported syntax, components, mappings, interpolation, or limits. |

Indeterminate takes precedence over unsafe. Both kinds of findings remain in the
report; exit 4 must not hide a known blocking issue from review. Exit 0 is not a
clean certification of arbitrary pipelines, deployment security, observed values,
secret strength, private directory permissions, or actual traffic coverage.

## Supported Subset

Checks follow the `genaisketch` connector conventions from collector v0.3.0.
OTLP receivers with explicit gRPC/HTTP protocol maps, batch and basic
memory-limiter processors, Prometheus exporters, and basic OTLP/HTTP or OTLP
exporter settings are recognized. Memory-limiter percentage bounds and the
percentage spike-versus-limit rule follow the
[OpenTelemetry Collector v0.161.0 validation](https://github.com/open-telemetry/opentelemetry-collector/blob/v0.161.0/internal/memorylimiter/config.go).
A nonzero `limit_mib` takes precedence over percentage settings; configured
percentage fields are still validated. An omitted spike limit defaults to 20%
of the effective limit. These checks are static and do not probe host memory or
certify that a chosen limit is sufficient. Trace and metric pipeline references, signal
direction, connector inputs/outputs, duplicate references, and unused components
are checked. Multiple connector instances or connector input/output pipelines
need an independent disjointness review and yield indeterminate.

Known components still have deliberately restricted settings. TLS files,
authentication extensions, arbitrary resource/attribute processors, transform
languages, sampling, routing, custom components, logs pipelines, provider
interpolation, and includes are not evaluated. Unrecognized settings remain
indeterminate even when the component itself is known. Basic endpoint/header
syntax is checked without certifying transport security or revealing values.

Connector checks include:

- Plaintext slices using sensitive or high-cardinality sources, including an
  `enduser.id` metric label, are blocking. Custom sources remain indeterminate.
- Slice overlap with all default and explicitly configured hashed sources is
  blocking, including span and resource candidates. Defaults are retained
  conservatively because map-merge behavior depends on the collector version.
- Explicit model operations are required for review: `chat`, `generate_content`,
  `text_completion`, and `embeddings` are recognized. Tool/agent operations and
  empty filters are blocking; unknown operations are indeterminate. The
  connector's missing-operation/model-attribute compatibility fallback still
  requires checking actual captures. No parent-span inheritance is inferred.
- Known hashed field sources, domains, canonicalization, numeric bounds,
  profiles, optional top-k selection, summary settings, and token source
  direction are checked. Custom extraction mappings need instrumentation review.
- Cache/reasoning subsets must not become aggregate token sources. Unknown
  source aliases are indeterminate. Missing usage is not inferred or replaced.
  Enabled approximate deduplication is outside the supported subset.

Recognized bounded label candidates include model, provider, operation, service,
environment, and team. Their actual values may still be sensitive or unbounded.
Neither low cardinality nor a recognized attribute name grants permission to
publish values. Use `inspect` on an authorized capture to review coverage.

## Input Limits

The reader accepts one regular file or stdin, capped at 256 KiB. It rejects
symlinks, including intermediate directory symlinks, directories, and special
files. It composes the existing confined local-file reader and checks directory
identity while opening. Use a canonical nonsymlink path, or explicit stdin.

Stable YAML v3 decodes into a syntax tree only, never reflected configuration
values. Aliases are never expanded. The byte cap limits bytes delivered to the
parser, not memory allocation; the parser can allocate the whole syntax tree
first. The parser has its own 10,000-level flow/block nesting ceiling.
Before any configuration inspection, the tree must have at most 8,192 nodes and
depth 32. Thus the smaller node/depth limits are validation limits after syntax
tree parsing, not streaming allocation limits. Anchors/aliases, merge keys,
duplicate decoded keys, complex/non-string keys, and multiple documents are
rejected. At most 128 findings plus one truncation finding are returned.

## Reviewed Shadow Proposal

```sh
fleetdiff diagnose --shadow-config collector.yaml
```

This opt-in prints only a fixed standalone YAML proposal to stdout, with the
input diagnosis on stderr. It cannot be combined with `--format`. Its exit code
still describes the input; generating a proposal does not approve it. Rejected
YAML and read failures produce no proposal. No input value, custom name,
endpoint, secret, or mapping is copied into the proposal.

Keep the existing collector configuration and backend unchanged. Add a separate
shadow branch from the authorized source to a new local collector. **Do not
redirect this command over the current configuration or merge the proposal as
an overlay.** If saving it, select a new private file that does not already exist.

Review the proposal's loopback ports, label values, operation/field mappings,
`REVIEW_SCOPE`, and `REVIEW_KEY_ID`. The reference `secret_env:
GENAI_SKETCH_SECRET` names an environment variable; it is not a secret value or
`${env:...}` interpolation. Supply an approved secret separately to the collector.
Create `./private-shadow-summaries` with mode 0700 and exactly one writer. The
proposal uses one producer, `shadow`, and `topk: 0` to disable frequent-items
state and top-k logs. Counters and distinct sketches remain available; user and
session rankings are not promised. Summaries remain pseudonymous, unsigned, and
unencrypted, and require controlled access.

With a trusted released collector executable, validate the **reviewed proposal**
before running it:

```sh
otelcol-genai-sketches validate --config reviewed-shadow.yaml
```

Validation is not a running collector or a coverage test. A separate opt-in test
validates the exact embedded proposal without importing collector runtime code:

```sh
FLEETDIFF_TEST_COLLECTOR=/path/to/trusted/otelcol-genai-sketches \
  go test ./internal/diagnose -run TestShadowCollectorValidation -count=1
FLEETDIFF_RELEASE_BINARY=/path/to/packaged/fleetdiff \
  go test ./internal/cli -run TestReleaseBinaryDiagnose -count=1
```

Validation evidence, October 4, 2026: the exact embedded proposal passed the
published collector v0.3.0 image's `validate` command, using `--network=none`,
`--read-only`, all capabilities dropped, and a read-only configuration mount.
The image digest matched the public release's `image-digest.txt`:
`sha256:c7fef29869ada99345725cf8504c8bd265357f570a23f1344769d362e595211c`.
This confirms configuration acceptance, not live receipt, export completeness,
or a production shadow trial.

After a separately reviewed shadow trial, preserve two completed windows in
separate private directories before collector retention expires:

```sh
fleetdiff investigate --before ./before --after ./after --expected shadow
```

Confirm `shadow` from independent trusted inventory. Do not infer the expected
set from whatever summary files arrived. One collector must observe each request
once; multiple producers need disjoint requests and compatible scope, key,
accounting, and window definitions. Matching metadata does not authenticate
senders, prove equal secrets, or establish instrumentation completeness.

## Finding IDs

IDs are stable within JSON version 1. Descriptions contain fixed vocabulary.

| Blocking | Unsupported / indeterminate |
| --- | --- |
| `sensitive_slice` | `unsupported_yaml` |
| `high_cardinality_slice` | `unsupported_component` |
| `hashed_source_overlap` | `unsupported_mapping` |
| `missing_operation_filter` | `unsupported_field` |
| `unsafe_operation_filter` | `unresolved_interpolation` |
| `unsafe_token_mapping` | `unknown_slice_source` |
| `unsafe_hashing` | `unknown_operation` |
| `pipeline_wiring` | `unknown_field_source` |
| | `unknown_token_mapping` |
| | `multiple_producers` |
| | `missing_connector` |
| | `findings_truncated` |

## Dependency Review

The only new runtime module is `go.yaml.in/yaml/v3 v3.0.5`, with no new
transitive runtime dependencies or collector runtime packages. Checked October
4, 2026: v3.0.5 is a stable release. The YAML organization keeps v3 on
security-fix maintenance; ongoing features are in v4. The checker uses a
byte-bounded, non-expanding syntax tree. See the
[primary maintenance policy](https://github.com/yaml/go-yaml#version-intentions)
and [v3 package documentation](https://pkg.go.dev/go.yaml.in/yaml/v3@v3.0.5).

At that check, the project's [security page](https://github.com/yaml/go-yaml/security)
had no published advisories, and the [official Go module vulnerability index](https://vuln.go.dev/index/modules.json)
had no entries for the maintained v3/v4 paths. The historical
[GO-2022-0603](https://pkg.go.dev/vuln/GO-2022-0603) affects the original
`gopkg.in/yaml.v3` before its May 2022 fix, not the selected release. Absence of
published advisories is not a security guarantee; repeat the release dependency
scan and retain hostile-input tests on upgrades.

## Next Step

Fix blocking findings and review indeterminate findings before rollout.
Check unsupported mappings against actual
instrumentation and the intended released collector before a separate shadow
trial. The [fixture demo](../examples/diagnose/README.md) exercises the local path.
