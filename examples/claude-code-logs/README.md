# Claude Code Logs: Private File Capture

Compare client-recorded token usage and cost across two periods, grouped
by user or owner-and-session, with accounting coverage and comparison quality checks.

This is a **local-checkout prototype**, not a capability of the released v0.8.0
binary. Use the standard [installation and verification guide](../../docs/OPERATIONS.md#install-and-verify)
for releases; do not assume an installed release accepts `--claude-code-logs`.
The commands below use `go run` from this checkout after its importer is built.
Account-auth conformance remains open; synthetic account/email tests do not
establish authenticated client behavior. Keep both capture routes prototype-only.

The unit is an **observed `api_request` record**, with **finality unknown**.
Error and retries-exhausted companion counts stay separate. Interruption can
produce populated but partial usage; cancellation can emit no API record.
Neither a successful transport nor a gateway success flag proves final usage,
complete traffic or provider billing. Prompts, sessions and sequence numbers
are not unique request keys. Exact-copy filtering is not semantic deduplication.

## Try A Fresh Synthetic File

From the repository root, using an already-installed Go toolchain and cached
dependencies (these commands deliberately disable downloads):

```sh
umask 077
demo=$(mktemp -d)
python3 -B examples/claude-code-logs/fixtures.py --output "$demo/logs.jsonl"
GOTOOLCHAIN=local GOPROXY=off go run ./cmd/fleetdiff investigate \
  --claude-code-logs "$demo/logs.jsonl" \
  --before-period 2026-10-11T00:00:00Z/2026-10-12T00:00:00Z \
  --after-period 2026-10-12T00:00:00Z/2026-10-13T00:00:00Z \
  --group-by user --format json
```

The deterministic generator uses invented identities and emits one request in
each period with fictional model label `synthetic-model-a`, plus two error
companions in the second. This is not an available-model claim. Each request has ordinary
input 7, cache read 11, cache creation 5 and output 4: inclusive input 23 and
total 27. Decimal cost is `0.00003435`, not the rounded/truncated micros field 34.
Missing cache/usage fields are not zero. The generator is not a native-client
capture and does not provide provider-reported provenance.

The example pins both disjoint, equal-duration periods. Select `--group-by
session` for owner-and-session attribution; session IDs alone are not global
identities. User attribution prefers account UUID, then email, then installation
ID, never a people count. Conflicting event/resource identities remain visible
for the importer to reject. Prompt quality uses owner, session and prompt IDs.
Add `--deduplicate-exact` only to request exact copied-record filtering, and use
`--format text` for the text report. Without a persistent key, hashes share only
this invocation's ephemeral scope. `--hash-secret-env VARIABLE` requires
`--source-scope APP`; provide secrets through your existing secret-management
process, not literals in commands, fixtures or source control.

## Importer Inputs And Limits

The prototype accepts repeated
`--claude-code-logs FILE` flags. With no period flags, it selects the latest two
complete seven-day UTC periods within the observed timestamp span when possible;
otherwise it labels two equal observed-span halves. Neither selection proves
complete export coverage. Supply both period flags to override; a single timestamp
cannot form an inferred comparison. The original capture suite uses explicit
periods; the additional rotation suite checks both inferred selections and
repeated-file copy handling against the local-checkout binary. These smoke checks
do not replace importer tests for aggregate limits and byte-snapshot consistency.

Importer limits: **256 files**, **128 MiB across the file set**,
**8 MiB/OTLP envelope**, **1,000,000 log records across files**,
**250,000 each of attributed groups and scoped prompts**, and **128 distinct values
each for models, client versions and capture profiles**. Rotation does not increase
these aggregate limits. Use an immutable, stopped snapshot of the selected files.

## Capture Route 1 Only

Prerequisites: Python 3.10+, local Docker Engine 28+ / current Docker Desktop,
and these two images already installed. The helper uses `--pull never` and fails
if either image is absent; it never downloads dependencies or images.

| Role | Pin |
| --- | --- |
| Collector Contrib v0.162.0 | `otel/opentelemetry-collector-contrib@sha256:39923a8e431bd1f57be82411999d389fcfe40857492e4365456d97a4c1f74be6` |
| Stdlib transport relay | `python@sha256:593bd06efe90efa80dc4eee3948be7c0fde4134606dd40d8dd8dbcade98e669c` |

```sh
umask 077
capture_parent=$(mktemp -d)
python3 -B examples/claude-code-logs/capture.py "$capture_parent/capture" --seconds 30
```

The temporary parent works on Linux and macOS; its `capture` child must not yet
exist. Choose a **new directory outside the checkout**. The helper prints a random
`http://127.0.0.1:PORT/v1/logs` endpoint. For an independently authorized client
session, point only its logs exporter at that endpoint, use `http/json`, and
keep prompt, assistant, tool-content and raw-body logging off. Do not enable
enhanced beta tracing for this recipe. Example settings, with the printed port:

```sh
export CLAUDE_CODE_ENABLE_TELEMETRY=1
export OTEL_LOGS_EXPORTER=otlp
export OTEL_EXPORTER_OTLP_PROTOCOL=http/json
export OTEL_EXPORTER_OTLP_LOGS_PROTOCOL=http/json
export OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://127.0.0.1:PORT/v1/logs
export OTEL_LOG_USER_PROMPTS=0 OTEL_LOG_ASSISTANT_RESPONSES=0
export OTEL_LOG_TOOL_DETAILS=0 OTEL_LOG_TOOL_CONTENT=0 OTEL_LOG_RAW_API_BODIES=0
unset CLAUDE_CODE_ENHANCED_TELEMETRY_BETA
```

This does not start Claude or make model calls. Restore the client's previous
settings after the authorized interval; do not replace an existing production
export destination without approval. The recipe supports only OTLP HTTP/JSON
logs, without compression or chunked transfer, at most 1 MiB per request. It is
not an OTLP gRPC/protobuf, traces, metrics, Loki, Elastic or Datadog adapter.

The collector and relay share an **internal Docker network** with no published
container ports and no external exporter. A serial host stdlib HTTP relay binds
only `127.0.0.1` and passes bounded request bodies through `docker exec` stdin;
it does not persist or print them. This accommodates Docker's internal-network
port-publishing restriction without adding an egress-enabled network. Raw data
exists in process memory before filtering. Local processes with access to the
port or Docker daemon are trusted; loopback is not authentication or a security
boundary against a compromised host. No credentials are required by this receiver.

## Privacy and Retention Contract

The [collector configuration](collector.yaml) applies these steps **before file
export**, failing the pipeline on transformation errors:

1. Retain only exact body strings `claude_code.api_request`,
   `claude_code.api_error` and `claude_code.api_retries_exhausted`. Unknown, map,
   array or arbitrary text bodies are dropped, even with an API-like attribute.
   Conflicting body/event-name pairs are dropped.
2. Keep only typed numeric accounting/sequence/status fields and approved
   attribution strings at log level. Remove raw errors, prompts, code, paths,
   model/query-source labels and unknown attributes. Wrong-type values are removed,
   not coerced to zero; numeric semantic validation belongs to the importer.
3. At resource level keep `service.version` only in bounded numeric `X.Y.Z` form
   and preserve `user.account_uuid`, `user.email`, `user.id`, `session.id` strings.
   Set controlled `service.name=claude-code` and, **after allowlisting**,
   `fleetdiff.capture.profile=claude-code-logs/v1`. The profile is a capture marker,
   not an assertion that the client version was independently verified.
4. Keep no scope attributes. Replace scope name/version, clear severity text and
   trace/span IDs, and set top-level event name from the accepted body. Pinned
   `groupbyattrs` compaction rebuilds resource/scope envelopes without schema URLs;
   the poisoned-URL test guards this version-specific behavior.

At both log and resource locations, any present `identity.source` is replaced
with the fixed string `unverified` and retained. Before removing an existing
wrong-type, empty or whitespace-only account UUID, email or installation ID,
the same marker is set. Even an empty OTLP value counts as present. Missing
identity fields alone do not set it. This prevents a deleted malformed preferred
identity from silently falling back to installation attribution; the importer
suppresses attribution for the unverified regime. Raw OIDC/custom regime content
is never copied into the capture.

Account-auth coverage remains unverified: the native mock evidence uses
installation attribution, and the account/email precedence and conflict checks
use synthetic identities. Neither proves field availability or account ownership
in authenticated Claude modes. No OIDC attribution profile is conformed.

Raw account/email/installation/session/prompt IDs are deliberately retained for
attribution, so **the capture remains private**, including values at both event
and resource locations. This is a field-based contract, not a semantic detector
for a sender hiding content inside an allowed identity string. Resource version
and profile changes are exposed by importer aliases and comparison warnings;
an absent marker means unknown. Capture filtering and importer-output privacy
are separate obligations. Do not share `logs.jsonl` or a poisoned test input.

The helper creates a `0700` directory and exclusively precreates `logs.jsonl` as
`0600`; `capture.json` is also `0600`. No rotation, dynamic filenames, compression
or appending to an old capture is used. Docker's hard file-size limit is **8 MiB**;
the timer is 1-300 seconds. Normal stop, SIGINT and SIGTERM stop/flush and remove
the containers/network. A cap hit or unexpected collector exit fails the helper;
the last JSON record can be partial, so do not silently salvage it as complete.
The manifest includes hashes, stop reason, retained-record count and unknown
finality/completeness. It is not a delivery-loss audit. Never import while writing.

Import the stopped `logs.jsonl` with the command above and periods appropriate to
that file. Keep captures only for the approved review interval, **at most 24 hours**,
then delete both files, the capture directory and its now-empty temporary parent.
Deletion is an operator obligation,
not an automatic TTL; do not leave a capture helper unattended. SIGKILL, host
failure or Docker failure can prevent cleanup: inspect and remove only this
invocation's `fleetdiff-claude-*` resources. Finite capture does not imply complete
window coverage or deletion of telemetry already sent to another backend.

## Existing Organization Collector

For longer collection, use the [long-running composition and operator gates](LONG_RUNNING.md)
with [collector-long-running.json](collector-long-running.json), layered after
the same redaction configuration. The short helper above remains limited to
300 seconds / 8 MiB. The long-running policy uses 8 MiB files, 15 backups and
15-day backup-age cleanup; it is not a time-triggered rotation or strict TTL.
At this pin rotated files become `0644` inside the required `0700` directory.
Strict per-file `0600` policies block this route. Account-auth conformance and
organization-specific receiver authentication remain deployment gates.

## Next Backend, Not Supported

**Loki operator-exported JSON is the next candidate**, not an available adapter.
Its [query-range API](https://grafana.com/docs/loki/latest/reference/loki-http-api/#query-logs-within-a-range-of-time)
provides a concrete stream export contract, but [OTLP ingestion mapping](https://grafana.com/docs/loki/latest/send-data/otel/)
changes field locations/names. Before support, pin a deployed version and prove
identity/profile locality, numeric precision, structured metadata, pagination and
same-timestamp boundary coverage using sanitized fixtures. No backend credentials,
queries or support are added here; Elastic and Datadog remain deferred.

## Verify Without A Client Or Provider

```sh
python3 -B examples/claude-code-logs/test_capture.py
FLEETDIFF_CLAUDE_CAPTURE_DOCKER=1 \
  python3 -B examples/claude-code-logs/test_capture.py
```

The first command runs local checks and explicitly skips Docker checks. The
second uses the exact pinned collector and locally cached relay image, invented
OTLP fixtures and no real client/provider. It tests poisoned resource/scope
attributes and URLs, body rejection, typed values, profile insertion, preserved
identity conflicts, missing versus zero, duplicate retention, loopback/internal
isolation, file permissions, the hard cap, timer and signal cleanup. Private
evidence and hashes are created outside git; `FLEETDIFF_CAPTURE_EVIDENCE` may
select a **new** private output directory. Importer-output sentinel tests are
maintained separately with the importer, not duplicated here.

For a smoke check against the built importer in this checkout, also set
`FLEETDIFF_CLAUDE_CAPTURE_CLI=1` and `FLEETDIFF_BIN` pointing to the built binary.
Without `FLEETDIFF_BIN`, it falls back to local `go run` (default proxy off),
preserving supplied `GOTOOLCHAIN`, `GOCACHE` and checksum settings; a missing cache
path defaults inside the private temporary evidence directory. It checks
the fresh model fixture and the actual filtered file, including user/session and
JSON/text modes plus optional exact-copy filtering. The clean fixture exercises
model comparison through direct file import only. The default privacy capture
intentionally removes model labels and therefore has no per-model breakdown:
it reports missing model coverage instead of retaining custom model strings.
It does not fabricate a model for those records.

The separate long-running checks exercise the exact rotation policy with fresh
synthetic logs and timestamped backup seeds, not a 15-day soak:

```sh
FLEETDIFF_CLAUDE_CAPTURE_DOCKER=1 \
  python3 -B examples/claude-code-logs/test_rotation.py
```

They verify pinned-binary configuration acceptance, rejection of append plus
rotation, two real size rotations, backup-age/count cleanup, redaction, JSON
record preservation and the `0644` limitation. Evidence is private and outside
git, using the same `FLEETDIFF_CAPTURE_EVIDENCE` convention. Also set
`FLEETDIFF_CLAUDE_CAPTURE_CLI=1` and `FLEETDIFF_BIN` to the updated binary to test
multi-file import of the actual rotated files, copied-file retention versus exact
filtering, and both default-period selections. No credentials or client are used.

Primary references: [pinned transform processor](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/v0.162.0/processor/transformprocessor/README.md),
[filter processor](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/v0.162.0/processor/filterprocessor/README.md),
[groupbyattrs log processing](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/v0.162.0/processor/groupbyattrsprocessor/processor.go),
[file exporter](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/v0.162.0/exporter/fileexporter/README.md),
and [Claude Code monitoring](https://code.claude.com/docs/en/monitoring-usage).
