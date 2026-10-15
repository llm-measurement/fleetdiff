<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Code authors: Vijay and Codex -->

# LiteLLM Spend Export Checks

Build the current CLI and run Python 3.10+ standard-library tests from the
repository root:

```sh
go build -o bin/fleetdiff ./cmd/fleetdiff
FLEETDIFF_BIN="$PWD/bin/fleetdiff" \
  python3 -B -m unittest discover -s examples/litellm-spend -p 'test_*.py' -v
```

## Pinned Database Integration

First pull the immutable references in [images.json](images.json):

```sh
docker pull ghcr.io/berriai/litellm@sha256:625981c83410a3ea68eb0697590a57ec1d764d634514d54fa5db0591077ee839
docker pull postgres@sha256:3645570cccdfa447589da9f57dd740faa29b30938e861289a5574b6ca6b03826
FLEETDIFF_SPEND_DOCKER=1 FLEETDIFF_BIN="$PWD/bin/fleetdiff" \
  python3 -B -m unittest discover -s examples/litellm-spend -p 'test_*.py' -v
```

The LiteLLM image reports 1.104.0; the PostgreSQL image reports 17.11. The
PostgreSQL multi-platform index was resolved from the official `17-bookworm`
image on October 10, 2026. Runtime versions, image digests, and the LiteLLM
writer/schema source hashes are checked. There are no implicit pulls in tests.

Chat Completions and text completion use writer-ready usage dictionaries with
positive, explicit-zero, and absent nested cache/reasoning details. Native
Messages and Responses first pass through the pinned logging usage
conversions, including the Responses standard-logging fallback. Messages uses
the canonical logging operation used by the pinned adapters; the writer is
exercised with both synchronous and asynchronous stored call-type names.
The test calls the real image's `get_logging_payload`, inserts its synthetic
payloads into an actual PostgreSQL table, then runs the public SQL recipe as a
SELECT-only role, once for default CSV and separately for optional JSONL.
JSON arrays for reader parity are generated locally from CSV, never by a
database aggregate. [test-schema.sql](test-schema.sql) mirrors the relevant
pinned spend-column types; it is not LiteLLM's entire migration suite. This
tests the writer payload and export contract, not HTTP routing, the gateway's
asynchronous database writer, provider support for text-completion cache fields,
or a paid provider. Both stored sync/async spellings are checked, not executed
as endpoint calls. The [observed behavior table](README.md#usage-details-default-on)
is checked against the same expected values as the real database tests.

**Version-pin warning:** these observations apply only to LiteLLM v1.104.0 and
the checked image/source hashes. Re-run the writer and database probes before
trusting these metadata paths on another version or configuration.

Both containers use `--network=none`, no published ports, no inherited provider
credentials, and read-only roots. PostgreSQL uses a unique Docker named volume,
not a database directory in the checkout or a synced tree. Temporary exports
live in container temporary storage and a private host temporary directory.
The test removes its containers and volume on normal completion or failure.
An externally killed test may need its `fleetdiff-spend-*` resources removed
manually; do not prune unrelated Docker resources.

## Coverage

### Daily Row Reconciliation

`test_row_coverage_runtime.py` runs the actual pinned proxy, a loopback mock
provider, and both asynchronous PostgreSQL writers against the image's full
Prisma schema. PostgreSQL has `--network none`; the proxy, mock provider and
client share only that container's loopback namespace. No paid provider,
external network, source-only inference or directly inserted spend/daily row
is used. Request clock references are controlled to test midnight; persistence
and accounting code are unchanged. Error logging is tested in its default,
explicitly enabled and disabled configurations.

| Runtime case | SpendLogs row increment | api_requests | successful_requests | failed_requests |
| --- | ---: | ---: | ---: | ---: |
| Successful non-streaming request | 1 | 1 | 1 | 0 |
| Successful streaming request | 1 | 1 | 1 | 0 |
| Response-cache hit (no provider forward) | 1 | 1 | 1 | 0 |
| Failed request, default/enabled error logging | 1 | 1 | 0 | 1 |
| Failed request, error logging disabled | 0 | 0 | 0 | 0 |
| Two forwards with the same provider response ID, combined | 1 | 2 | 2 | 0 |
| Request with no user identity | 1 | 1 | 1 | 0 |

A request starting before midnight and ending afterward increments the start
day's UTC counters. The second collision request returns HTTP 200, reaches the
provider and increments the daily table while its duplicate SpendLogs primary
key loses a row. The [recorded fixture](coverage_runtime_expected.json) binds
the observations and synthetic exports to image, source, SQL and harness hashes.
`coverage_runtime_fixture.py` checks the exported fixture; `test_row_coverage.py`
checks both CLI modes, unchanged accounting and source-safe output. The optional
daily SQL export uses a SELECT-only role and the same read-only snapshot as the
request export. Omitting the CLI option preserves the v0.8.0 output goldens.

See [row coverage and its limits](../../docs/ROW_COVERAGE.md). These runtime
cases pin one chat-completions profile with retries disabled, not every endpoint
or deployment configuration.

### Request-Level Accounting

- The original 20-row fixture has identical CSV, JSON, and JSONL records.
  Request, token, zero-only, and failure arithmetic is checked separately
  from the actual importer output.
- The actual writer adds absent usage, partial failures, input-only mismatches,
  logging fallback, discarded arbitrary provenance markers, and UTC boundary
  cases. Accepted Chat Completions, text completions, native Anthropic Messages,
  and Responses operation names are exercised. Unpinned operations are inventoried
  separately and make the selected-period comparison incomplete.
  Expected export membership comes from actual writer timestamps and the
  half-open UTC bounds, not a fixed row count or database row order.
- The real writer produces the usage detail metadata. SQL aliases
  are compared to each actual payload's pinned metadata paths, including native
  usage details and absent values. Base token counts must remain unchanged;
  cache and reasoning details are not added to the writer's normalized totals.
  No details are invented in the DB. Default export and explicit
  `usage_details=true` must match; `usage_details=false` omits all three detail
  columns without changing the base projection or selected rows.
- The observed behavior table covers positive, zero, and absent details for
  all eight supported stored names. Nested Chat/text zeros do not populate
  top-level cache aliases; Messages normalizes absent cache fields to zero;
  Responses omits zero cache aliases, cache write, and exported reasoning even
  when native reasoning is supplied. Missing writer paths remain SQL NULL,
  empty CSV fields, and literal JSONL null. Entirely missing usage is checked
  independently from a usage object with missing details.
- Native Messages input 100 plus cache read 80 and creation 20 becomes inclusive
  prompt 200 exactly once. Responses input 200 is already inclusive. Both retain
  output 10 and total 210 through SQL and importer; subset counters must not
  inflate recorded tokens. The normalizer's synthetic zero is not proof of a
  provider-reported zero or independently observed cache behavior.
- Only synthetic ignored content is added directly to the test table, proving
  that the projection excludes it. The default SQL invocation must create only
  CSV; setting `jsonl_file` must create only JSONL, with or without `csv_file`.
  These checks cover both default-detail and opt-out projections. Records match
  across both SQL formats and the locally assembled JSON array. Empty intervals
  also produce valid files. A fixture-only check rejects database sorting,
  JSON-array aggregation, and multiple CSV `COPY` passes in the recipe.
- Fixed-secret JSON reports match for all five groups, with and without
  displayed hashes. Importer totals and individual usage-detail counters are
  checked against exported row arithmetic without adding subsets to totals.
- Sentinels check stdout, stderr, JSON, and errors for raw keys, owners, teams,
  end users, sessions, model aliases, code, content, paths, and the test secret.
  Tests check source preservation and absence of unexpected importer artifacts.
  Assertion failures never replay raw child output or row dictionaries.
- Malformed numbers and spend remain quality evidence; malformed timestamps,
  broken JSON, duplicate requests, aggregate exports, and missing paths fail
  without exposing source values. Generator tests cover determinism, exclusive
  private output, unique keys, and shared-period key pools.

Missing `FLEETDIFF_BIN` skips CLI checks; missing `FLEETDIFF_SPEND_DOCKER=1`
skips database checks. A skipped suite is not end-to-end evidence.

## Rejection Projection

The pinned failure hook and spend payload writer generate the three positive
`rejection_kind` shapes and retain the `agent_id` sentinel from request metadata.
Supplying an auth-object agent alone did not populate the writer column in this
probe; the export does not recover absent identities. Only database persistence
is replaced with an in-process capture; no provider or HTTP route is called.
The same read-only SQL runs over those payloads in PostgreSQL and 46 negative
mutations: wrong code/class/category/type, conflicting type, custom or wrapped
prefix, missing message/status, success status, absent/empty/nonobject metadata,
missing error information and normalized-label-only evidence. The non-session
budget case also rejects a substituted 429 code.

CSV, JSONL and locally assembled JSON must agree. Unknown classifications stay
literal JSONL null; cache opt-out preserves the classifier, agent and every base
column. Error-message/content sentinels must not reach exports; the raw agent
sentinel belongs in the private export but must not reach CLI output. These
checks establish a pinned projection, not complete deployed failure logging,
limiter enforcement, or a live gateway's persistence behavior.

## Released-Reader Compatibility

Point `FLEETDIFF_V070_BIN` at an independently installed released v0.7.0 binary
(the test verifies its reported version). Run from the repository root:

```sh
go build -o bin/fleetdiff ./cmd/fleetdiff
FLEETDIFF_SPEND_DOCKER=1 \
FLEETDIFF_BIN="$PWD/bin/fleetdiff" \
FLEETDIFF_V070_BIN="$PWD/fleetdiff-install/fleetdiff" \
  python3 -B -m unittest discover -s examples/litellm-spend -p 'test_*.py' -v
```

The compatibility tests compare synthetic exports and actual DB exports with
and without `agent_id`/`rejection_kind`. They cover CSV/JSON/JSONL, all five
existing groups, hashes on/off, text/JSON output and DB cache-details opt-out.
The baseline removes only the two additive columns. Existing fixture files
remain unchanged and continue testing old-export column absence. No new CLI
feature is implied by released-reader compatibility. Without
`FLEETDIFF_V070_BIN`, these compatibility tests are explicitly skipped.

### Export-Only Verification, October 11, 2026

Before the session-budget fixture tests were added, the export-only suite
passed **23 tests, no skips**, using both immutable
images in `images.json` and an independently installed released binary for both
CLI variables. The binary identified itself as
`fleetdiff v0.7.0 (25694f060c01af075a612b2ec1c67482a9c6c695) go1.26.9 darwin/arm64`;
its SHA-256 was
`bce06af5b2011feaa7fcacfc65e867adc8a1d56bdb6436bc4dce0ee14c01e541`.

The 20-row synthetic compatibility export and the 69-row database export
(three positive shapes, 46 negative mutations, 20 ordinary model rows) passed
every released-reader comparison above. The separate existing writer/usage
database matrix also passed. No raw error/content sentinel escaped the SQL
projection, and no raw agent sentinel escaped the CLI. Containers and named
volumes were removed by the harness; no paid calls or network access were used
by either test container. This does not verify a new session-budget CLI build,
live proxy persistence, logging completeness, or enforcement effectiveness.

**Judgment: positive for the additive export and v0.7.0 compatibility.** The
integrated session-budget checks below extend this earlier export-only run.

## Synthetic Session-Budget Fixture

The newly authored Apache-2.0 [session-budget.csv](session-budget.csv) keeps
four complete sessions separate from missing-spend and old-column test
variants. Its 13 records include 12 model calls and one session-budget
rejection. The independent fixture arithmetic checks totals of USD
0.20/1.10/1.20/1.60 and call counts 1/6/2/3. Its companion
[license notice](session-budget.csv.license) avoids comment rows in the CSV.

`test_session_budget.py` invokes the real CLI with `--group-by session`, an
explicit half-open UTC `--period`, and `--session-budget-usd 1.00`. It checks:

- Three of four complete sessions exceed the cap; two have no recorded
  session-budget rejection. The classified rejection is not a model call.
- One session spans 4,000 seconds and two keys, two teams and two agents,
  without splitting that session into separate identity groups.
- Exact spend/call distributions with independent counts, CSV/JSON/JSONL
  report parity, source preservation and `--top` invariance.
- A missing-spend variant keeps three complete totals and a separate USD 1.20
  partial subtotal above cap; all four sessions remain in call distributions.
- Old-column variants preserve totals, make rejection coverage unknown, and
  do not invent agent identities when `agent_id` is absent.
- Agent sentinels remain absent from text, JSON and errors, with hashes on/off.
  Unknown rejection values, conflicting status, malformed timestamps and
  duplicate request IDs fail without partial report output or raw diagnostics.

Build the current source and run the **same full pinned DB suite** with
`FLEETDIFF_BIN` pointing to that build and `FLEETDIFF_V070_BIN` pointing only to
the independent released compatibility binary:

```sh
go build -o bin/fleetdiff ./cmd/fleetdiff
FLEETDIFF_SPEND_DOCKER=1 FLEETDIFF_BIN="$PWD/bin/fleetdiff" \
FLEETDIFF_V070_BIN="$PWD/fleetdiff-install/fleetdiff" \
  python3 -B -m unittest discover -s examples/litellm-spend -p 'test_*.py' -v
```

### Integrated Verification, October 11, 2026

The full suite passed **29 tests, no skips** in 20.967 seconds with the source
binary built using Go 1.26.9 and `GOTOOLCHAIN=local`. `FLEETDIFF_BIN` selected
that build; only `FLEETDIFF_V070_BIN` selected the independently installed
v0.7.0 binary identified above. Both pinned images were already present, and
both test containers ran with networking disabled. The six new fixture/CLI
tests passed alongside all existing writer/DB, privacy and compatibility
checks. No paid calls were made.

**Judgment: positive for the integrated synthetic session-budget example and
the pinned export/reader checks.** This is not evidence of live enforcement or
complete deployed failure logging. Next, retain these fixtures as regression
checks and rerun the suite after subsequent CLI/report changes; no broader
provider test is needed for this example.

## Scale Measurement

[generate.py](generate.py) streams the reviewed synthetic pattern, preserving
timestamps and token arithmetic while making request and session IDs unique.
It retains only the 20-row template and current row. By default the three-key
pattern repeats. `--unique-keys` assigns a different key to every row, with no
cross-period overlap. `--key-cardinality N` cycles a pool independently in each
period, giving shared keys and repetition even when N is a multiple of 20.
The two options are mutually exclusive. Owners and teams stay low-cardinality;
the model count is always two.

```sh
python3 -B examples/litellm-spend/generate.py --rows 10000000 --format csv \
  --unique-keys --output /tmp/litellm-ten-million-unique.csv
bin/fleetdiff investigate --litellm-spend /tmp/litellm-ten-million-unique.csv \
  --before-period 2026-10-07 --after-period 2026-10-08 \
  --group-by key --format json
```

Measure the CLI separately from generation. `--output` creates a new mode-0600
file and refuses to overwrite; omit it to stream to stdout. Choose CSV, JSON,
or JSONL with `--format`. `--rows 10000001` creates a row-limit rejection case,
not an accepted import size.

The review target is ten million rows within 180 seconds for CSV, 360 seconds
for JSON/JSONL, and 1 GiB peak RSS on an M4 Max. The
[recorded measurements](../../docs/BENCHMARKS.md#litellm-spend-import-source-checkout)
include the earlier one-million-row baseline. Add `--days 16` to generate a
default-period trial; omit the importer's period flags for that run.
For new runs, record the exact
CLI build, format, row count, identity pattern, explicit windows, elapsed time,
and operating-system peak RSS. Generation and small-fixture test duration are
not importer throughput evidence.
