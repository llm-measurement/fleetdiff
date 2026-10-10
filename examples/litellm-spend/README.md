<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Code authors: Vijay and Codex -->

# Investigate A LiteLLM Spend Export

**Recorded tokens doubled in this synthetic example. One model explains the
increase, and two keys account for all of it.** The report separates request
count from request size where the usage records support that comparison.

This command requires a source build; it is not in a released binary. From the
repository root, with Go 1.26 at a current security patch:

```sh
go build -o bin/fleetdiff ./cmd/fleetdiff
bin/fleetdiff investigate --litellm-spend examples/litellm-spend/synthetic.csv \
  --before-period 2026-10-07 --after-period 2026-10-08 --group-by key
```

Selected lines from an actual run on this fixture:

```text
Recorded tokens: 8000 -> 16000 (+8000); logged model requests: 8 -> 12.
  model-1: 4000 -> 12000 recorded tokens; 4 -> 6 requests.
    1000.00 -> 2000.00 recorded tokens per request; +3000 from request count, +5000 from request size.
  2 leading tracked keys account for 100.00% of the net recorded increase.
  Zero-only records, origin unknown: 2 -> 4.
  Failed records (overlap usage categories): 1 -> 2.
```

The per-model split is available for model-1; the quality counts point to rows
to review in the local LiteLLM logs. Try the same 20 safe, synthetic records as
[CSV](synthetic.csv), [JSON array](synthetic.json), or [JSONL](synthetic.jsonl).

## Export Request-Level Rows

Use [export.sql](export.sql) with `psql` and your existing read-only database
access. Configure a PostgreSQL service named `litellm_readonly` using approved
connection settings and your normal credential mechanism. From the repository
root, choose a new private destination directory outside the checkout:

```sh
umask 077
export_dir="$HOME/litellm-export"
mkdir "$export_dir"
psql 'service=litellm_readonly' -Xq -v ON_ERROR_STOP=1 \
  -v start_utc=2026-10-07T00:00:00Z -v end_utc=2026-10-09T00:00:00Z \
  -v csv_file="$export_dir/spend.csv" \
  -v json_file="$export_dir/spend.json" \
  -v jsonl_file="$export_dir/spend.jsonl" \
  -f examples/litellm-spend/export.sql \
  >"$export_dir/private-psql.out" 2>"$export_dir/private-psql.err"
```

The SQL projection is defined once and reused for all three files in a single
**REPEATABLE READ, READ ONLY transaction**. Outputs are local to the `psql`
client. See PostgreSQL's [transaction documentation](https://www.postgresql.org/docs/17/sql-set-transaction.html)
and [psql documentation](https://www.postgresql.org/docs/17/app-psql.html).

## Investigate Your Export

```sh
bin/fleetdiff investigate --litellm-spend "$export_dir/spend.csv" \
  --before-period 2026-10-07 --after-period 2026-10-08 \
  --group-by key --format json
```

Use `--group-by key|team|user|end-user|session`; model breakdowns are automatic.
`user` means key owner, while `end-user` means the application's end user.
`--top N` controls the contributor list. To reuse hashes across comparisons,
set your own strong secret in an environment variable and pass
`--hash-secret-env NAME`. Add `--show-hashes` to display them.

### Optional Usage Details

Add `-v usage_details=true` to the `psql` command for these projection aliases:

| Export alias | Path below `metadata.additional_usage_values` |
| --- | --- |
| `cache_read_input_tokens` | `cache_read_input_tokens` |
| `cache_write_input_tokens` | `cache_creation_input_tokens` |
| `reasoning_output_tokens` | `completion_tokens_details.reasoning_tokens` |

The pinned image's actual writer and SQL export are tested with values of 25,
10, and 5 respectively, and with absent details. See the
[pinned writer](https://github.com/BerriAI/litellm/blob/79645770fedc7ec2627e6468d31062f20f82aecc/litellm/proxy/spend_tracking/spend_tracking_utils.py#L732),
[schema](https://github.com/BerriAI/litellm/blob/79645770fedc7ec2627e6468d31062f20f82aecc/schema.prisma#L646),
and [integration checks](TESTING.md).

## Data And Limits

- Accepted inputs are request-level SQL projections as headered CSV, a JSON
  array, or JSONL. LiteLLM Usage dashboard CSV/JSON exports are daily aggregates:
  the importer recognizes them and requests row-level data. API page envelopes
  and session-grouped views are unsupported; no API export recipe is included.
- fleetdiff reads a local file without a collector, database connection, model
  key, or provider call. Counts describe supplied logged model requests, not
  proven provider sends. Hidden retries and missing records cannot be recovered.
- Supported call types are `completion`, `acompletion`, `text_completion`, and
  `atext_completion`. Embeddings, Responses, agent, tool, and other rows are
  counted separately as out of scope.
- Check your database's timestamp convention. The pinned schema stores UTC in
  `timestamp(3)` columns without a timezone; SQL emits explicit UTC RFC3339.
  Do not apply that interpretation to local-wall-time data. Requests belong to
  the half-open interval containing `startTime`. Retain `endTime`; absent end
  times withhold the per-request split.
- A date selects a UTC day. Explicit RFC3339 `start/end` intervals must be
  equal-duration, ordered, disjoint, and completed before now. Without period
  flags, the command chooses two trailing seven-day UTC windows inside the
  file's interior days: ceiling of the earliest `startTime` through floor of
  the latest, capped at today. This heuristic does not certify coverage.
- Zero-only usage has unknown origin. Failures overlap usage categories and
  retain valid partial tokens. Unclear usage withholds the whole-period split;
  unchanged recorded totals do not prove unchanged real usage.
- Prompt and completion tokens are added once; `total_tokens` is a cross-check.
  Optional details are subsets, not extra totals or physical spend-log columns.
  Missing details stay null, invalid casts fail the SQL export, and arbitrary
  metadata is not accepted as proof of provenance. These paths are version-pinned.
- Recorded spend is source accounting, not an invoice. Default-zero cost is
  ambiguous. No provider pricing, run rate, or forecast is added.
- Output aliases hide raw identities, including models; displayed hashes are
  linkable. Use a private secret for real exports, never the fixed test secret.
  Sessions are scoped to keys but may be generated trace IDs, not conversations.
- The SQL excludes prompts, responses, code, request bodies, IP addresses,
  API bases, and whole metadata objects. Exports and database diagnostics still
  contain sensitive information: keep them private. Use only successful exports;
  failed SQL can leave partial files. Retry into a new directory because `psql`
  can overwrite files. A consistent snapshot does not establish complete logging.
- Limits: **1,000,000 rows; 2 GiB/file; 64 KiB/row; 8 KiB/field; 64 fields/row;
  JSON depth 8; 128 models.** Limits also apply to ignored content. Select a
  narrower SQL interval when necessary. The recipe writes all three formats,
  uses a database-side aggregate for the JSON array, and has a two-minute
  per-statement timeout; review large exports with your database operator.

## Checks And Scale Inputs

Run fixture arithmetic, format equality, and privacy checks with Python 3.10+:

```sh
FLEETDIFF_BIN="$PWD/bin/fleetdiff" \
  python3 -B -m unittest discover -s examples/litellm-spend -p 'test_*.py' -v
```

For the actual pinned writer -> PostgreSQL -> SQL exports -> importer test,
follow [TESTING.md](TESTING.md). It includes image pulls, isolation, and coverage.

Generate streaming synthetic inputs without running an importer benchmark:

```sh
python3 -B examples/litellm-spend/generate.py --rows 1000000 --format csv \
  --output /tmp/litellm-million.csv
python3 -B examples/litellm-spend/generate.py --rows 1000000 --format csv \
  --unique-keys --output /tmp/litellm-million-unique.csv
python3 -B examples/litellm-spend/generate.py --rows 1000000 --format jsonl \
  --key-cardinality 100000 --output /tmp/litellm-million-shared.jsonl
```

`--output` creates a new mode-0600 file and refuses overwrites. `--unique-keys`
gives every row a different key; `--key-cardinality N` cycles a shared key pool
independently in each period. Use `--format json` or `jsonl` for other encodings.
Benchmark setup and resource measurements are in [TESTING.md](TESTING.md).
