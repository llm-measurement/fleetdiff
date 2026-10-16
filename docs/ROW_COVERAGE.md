<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Code authors: Vijay and Codex -->

# Are All Requests In The Spend Log?

**8 exported rows versus 9 daily requests: 1 missing row.** This synthetic
fixture reproduces a real response-ID collision through pinned LiteLLM's proxy
and database writers. The next UTC day matches: 1 row and 1 daily request.
The check leaves token and spend totals exactly as recorded in the export.

Install [fleetdiff v0.9.0](../README.md#2-install-v090). From the repository
root, select the verified binary for the commands below:

```sh
FLEETDIFF_BIN="$PWD/fleetdiff-install/fleetdiff"
```

## Export And Compare

Use the [read-only SQL recipe](../examples/litellm-spend/export.sql) with
read-only access to both `LiteLLM_SpendLogs` and `LiteLLM_DailyUserSpend`.
Configure the `litellm_readonly` PostgreSQL service through your existing
credential mechanism. The [coverage export helper](../examples/litellm-spend/export-coverage.sh)
creates a new private directory and exports both files in one repeatable-read,
read-only transaction:

```sh
umask 077
export_dir="$HOME/litellm-coverage-export"
sh examples/litellm-spend/export-coverage.sh "$export_dir" \
  2026-09-23T00:00:00Z 2026-10-09T00:00:00Z
"$FLEETDIFF_BIN" investigate --litellm-spend "$export_dir/spend.csv" \
  --litellm-daily-spend "$export_dir/daily.csv" --group-by key
```

Replace the historical bounds with your interval of complete UTC days. The
16-day export leaves room for the default two complete seven-day periods.
Use `--before-period` and `--after-period` for explicit periods. Session review
also accepts the option with `--period` or `--session-budget-usd`.

The daily projection sums `api_requests`, `successful_requests` and
`failed_requests` across all daily user/model/key rows for each date. Its CSV
contains only those three counts and `date`; no identities or content. The
request export is unchanged. The helper refuses an existing directory and uses
distinct inherited file descriptors so filename aliases cannot overwrite an
export. Its `daily_csv_file` SQL variable is internal to the helper, not a
general output-path option. Use the existing `export.sql` commands for a single
request export. Omit `--litellm-daily-spend` to keep the existing report byte for byte.

## Reading Coverage

Run the recorded fixture from the repository root:

```sh
"$FLEETDIFF_BIN" investigate \
  --litellm-spend examples/litellm-spend/coverage_runtime_spend.csv \
  --litellm-daily-spend examples/litellm-spend/coverage_runtime_daily.csv \
  --before-period 2026-10-07 --after-period 2026-10-08
```

The report begins:

```text
Spend-log row coverage (relative to LiteLLM daily request counts):
  2026-10-07 UTC: 8 exported rows; 9 daily requests (8 successful, 1 failed); 1 missing row.
  2026-10-08 UTC: 1 exported row; 1 daily request (1 successful, 0 failed); counts match.
Token and spend totals below use exported rows only.
```

All selected request rows count here, including failed records and call types
excluded from token analysis. UTC days follow request `startTime`, not receipt
or export time. A positive difference is reported as missing rows relative to
the daily request count. Extra rows are a discrepancy to review, never a
negative missing count. No token, request, spend or ranking total is imputed.

JSON adds `row_coverage.days`: `exported_rows`, `api_requests`,
`successful_requests`, `failed_requests`, `missing_rows` and `status`, with
`date_utc`. An absent daily row has null daily counts and unknown coverage.
Partial UTC days have null `missing_rows`; full-day counters cannot establish
coverage for a selected part of that day. A real exported zero remains zero.
Empty request exports can still be checked with explicit periods; the usage
comparison then says it has no analyzable requests.

## Limits

- Match application, date bounds, filters and retention in both inputs. Use one
  LiteLLM database scope and allow its asynchronous writers to settle before
  exporting completed days. A shared database snapshot aligns reads, not the
  completion times of separate writes.
- The daily table is a second accounting path, not an independent request
  oracle. Logging configuration, retention and lost writes can affect either
  path. Matching counts cannot reveal requests absent from both, establish
  complete token usage, or certify a provider invoice. A shortfall identifies
  records to investigate, not their cause.
- In the pinned runtime, disabling failure logging (`disable_error_logs=true`)
  omitted the failed request from **both** tables. Successful response-cache
  hits counted in both despite making no provider call. These are logged
  spend-transaction counts, not universal ingress or provider-send counts.
- The projection and runtime definitions are pinned to LiteLLM **v1.104.0** in
  [images.json](../examples/litellm-spend/images.json). Recheck other versions,
  logging configurations and routes before interpreting their counts. This
  check compares request records, not provider sends or retry attempts.
- Daily inputs are capped at 3,660 dates, 1 MiB and 512 bytes per CSV line.
  Duplicate dates, malformed counts and extra columns fail with source-safe
  errors. Dates with no daily row remain unknown rather than being filled in.

See [verification](../examples/litellm-spend/TESTING.md) and the
[request-level accounting contract](../examples/litellm-spend/README.md#data-and-limits).
