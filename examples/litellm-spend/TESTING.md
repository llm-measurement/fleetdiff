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

Native Messages and Responses first pass through the pinned logging usage
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
asynchronous database writer, or a paid provider.

Both containers use `--network=none`, no published ports, no inherited provider
credentials, and read-only roots. PostgreSQL uses a unique Docker named volume,
not a database directory in the checkout or a synced tree. Temporary exports
live in container temporary storage and a private host temporary directory.
The test removes its containers and volume on normal completion or failure.
An externally killed test may need its `fleetdiff-spend-*` resources removed
manually; do not prune unrelated Docker resources.

## Coverage

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
- The real writer produces the optional usage detail metadata. SQL aliases
  are compared to each actual payload's pinned metadata paths, including native
  usage details and absent values. Base token counts must remain unchanged;
  cache and reasoning details are not added to the writer's normalized totals.
  No details are invented in the DB.
- Only synthetic ignored content is added directly to the test table, proving
  that the projection excludes it. The default SQL invocation must create only
  CSV; setting `jsonl_file` must create only JSONL, with or without `csv_file`.
  These checks cover both default and optional-detail projections. Records match
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
