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

The test calls the real image's `get_logging_payload`, inserts its synthetic
payloads into an actual PostgreSQL table, then runs the public SQL recipe as a
SELECT-only role. [test-schema.sql](test-schema.sql) mirrors the relevant
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
  cases. Its four accepted operation names are exercised; other operations
  stay excluded. Two rows at the outer boundaries are omitted by SQL.
- The real writer produces the optional usage detail metadata. SQL aliases
  must yield exactly 25 cache-read, 10 cache-write, and 5 reasoning tokens on
  the one detailed row, and null elsewhere. No details are invented in the DB.
- Only synthetic ignored content is added directly to the test table, proving
  that the projection excludes it. Default and optional-detail exports match
  across CSV, JSON, and JSONL. Empty intervals also produce valid files.
- Fixed-secret JSON reports match for all five groups, with and without
  displayed hashes. Importer totals are checked against exported row arithmetic.
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
python3 -B examples/litellm-spend/generate.py --rows 1000000 --format csv \
  --unique-keys --output /tmp/litellm-million-unique.csv
bin/fleetdiff investigate --litellm-spend /tmp/litellm-million-unique.csv \
  --before-period 2026-10-07 --after-period 2026-10-08 \
  --group-by key --format json
```

Measure the CLI separately from generation. `--output` creates a new mode-0600
file and refuses to overwrite; omit it to stream to stdout. Choose CSV, JSON,
or JSONL with `--format`. `--rows 1000001` creates a row-limit rejection case,
not an accepted import size.

The predeclared target is one million CSV rows within 60 seconds and 512 MiB
peak RSS on an M4 Max, with both low and high key cardinality. Measure JSON
and JSONL separately. The [recorded measurements](../../docs/BENCHMARKS.md#litellm-spend-import-source-checkout)
meet that target. For new runs, record the exact
CLI build, format, row count, identity pattern, explicit windows, elapsed time,
and operating-system peak RSS. Generation and small-fixture test duration are
not importer throughput evidence.
