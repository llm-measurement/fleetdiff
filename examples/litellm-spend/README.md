<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Code authors: Vijay and Codex -->

# Investigate A LiteLLM Spend Export

**Recorded tokens doubled in this synthetic example. One model explains the
increase, and two keys account for all of it.** The report separates request
count from request size where the usage records support that comparison.

Install [fleetdiff v0.6.0](../../README.md#2-install-v060), then run this fixture
from the repository root with the verified binary:

```sh
./fleetdiff-install/fleetdiff investigate --litellm-spend examples/litellm-spend/synthetic.csv \
  --before-period 2026-10-07 --after-period 2026-10-08 --group-by key
```

Selected lines from an actual run on this fixture:

```text
Recorded tokens doubled (8,000 -> 16,000).
model-1 accounts for 100% of the net recorded increase (+8,000 tokens).
  model-1: 4,000 -> 12,000 recorded tokens; 4 -> 6 requests.
    1,000.00 -> 2,000.00 recorded tokens per request; +3,000 from request count, +5,000 from request size.
  2 leading tracked keys account for 100% of the net recorded increase.
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
  -v start_utc=2026-09-23T00:00:00Z -v end_utc=2026-10-09T00:00:00Z \
  -v csv_file="$export_dir/spend.csv" \
  -f examples/litellm-spend/export.sql \
  >"$export_dir/private-psql.out" 2>"$export_dir/private-psql.err"
```

The recipe writes **CSV only by default**, using one `COPY` pass with no
database sort, in a **REPEATABLE READ, READ ONLY transaction**. The historical
16-day interval leaves room for the importer's default two seven-day windows
inside the file's edge days. Sparse logs may still need explicit periods;
the interval alone does not certify coverage.

To select JSONL **instead of CSV**, replace the `-v csv_file=...` line with
`-v jsonl_file="$export_dir/spend.jsonl"`. Defining `jsonl_file` selects a single
row-at-a-time JSON query and suppresses CSV even if `csv_file` is also supplied.
The recipe never aggregates or exports a JSON array; the reader still accepts
existing JSON arrays. Output is local to the `psql` client. See PostgreSQL's
[transaction documentation](https://www.postgresql.org/docs/17/sql-set-transaction.html)
and [psql documentation](https://www.postgresql.org/docs/17/app-psql.html).

## Investigate Your Export

```sh
./fleetdiff-install/fleetdiff investigate --litellm-spend "$export_dir/spend.csv" \
  --group-by key --format json
```

For the optional JSONL export, use `"$export_dir/spend.jsonl"` as the input.
The small fixture above intentionally keeps its explicit one-day periods.

Use `--group-by key|team|user|end-user|session`; model breakdowns are automatic.
`user` means key owner, while `end-user` means the application's end user.
`--top N` controls the contributor list. To reuse hashes across comparisons,
set your own strong secret in an environment variable and pass
`--hash-secret-env NAME`. Add `--show-hashes` to display them.

### Usage Details (Default On)

The SQL includes these three projection aliases by default. Add
`-v usage_details=false` to the `psql` command to omit all three columns;
`-v usage_details=true` explicitly selects the default behavior.

| Export alias | Path below `metadata.additional_usage_values` |
| --- | --- |
| `cache_read_input_tokens` | `cache_read_input_tokens` |
| `cache_write_input_tokens` | `cache_creation_input_tokens` |
| `reasoning_output_tokens` | `completion_tokens_details.reasoning_tokens` |

Missing metadata paths stay SQL `NULL` (empty CSV fields, JSONL `null`), never
invented zeros. Present zeros remain zeros. This describes the stored writer
metadata, not necessarily the provider's original usage: the pinned writer can
omit or synthesize details as shown below. These are **version-pinned paths for
LiteLLM v1.104.0**, not a cross-version or cross-provider guarantee. Recheck the
writer and export before applying this recipe to a different version. See the
[pinned writer](https://github.com/BerriAI/litellm/blob/79645770fedc7ec2627e6468d31062f20f82aecc/litellm/proxy/spend_tracking/spend_tracking_utils.py#L732),
[schema](https://github.com/BerriAI/litellm/blob/79645770fedc7ec2627e6468d31062f20f82aecc/schema.prisma#L646),
and [integration checks](TESTING.md).

The following values are observed through the real pinned writer, PostgreSQL,
and default SQL export using synthetic responses. `Positive`, `Zero`, and
`Absent` describe the supplied usage details, not whether cache use occurred.

| Path | Supplied details | Spend prompt | Cache read | Cache write | Reasoning |
| --- | --- | ---: | ---: | ---: | ---: |
| Chat Completions | Positive | 200 | 80 | 20 | 5 |
| Chat Completions | Zero | 200 | NULL | NULL | 0 |
| Chat Completions | Absent | 200 | NULL | NULL | NULL |
| Text completion | Positive | 200 | 80 | 20 | 5 |
| Text completion | Zero | 200 | NULL | NULL | 0 |
| Text completion | Absent | 200 | NULL | NULL | NULL |
| Anthropic Messages | Positive | 200 | 80 | 20 | 0 |
| Anthropic Messages | Zero | 100 | 0 | 0 | 0 |
| Anthropic Messages | Absent | 100 | 0 | 0 | 0 |
| Responses | Positive | 200 | 80 | NULL | NULL |
| Responses | Zero | 100 | NULL | NULL | NULL |
| Responses | Absent | 100 | NULL | NULL | NULL |

Each row is checked for both stored spellings: `completion` / `acompletion`,
`text_completion` / `atext_completion`, `anthropic_messages` /
`aanthropic_messages`, and `responses` / `aresponses`. Chat/text probes supply
writer-ready usage dictionaries with nested `prompt_tokens_details` and
`completion_tokens_details`; this does not establish provider support for
those details on text completion. Messages and Responses use their native
logging conversions first. Messages uses the canonical adapter logging name
for both stored spellings; these are not live sync/async endpoint tests.

The nested Chat/text cache aliases are copied only when positive. Messages
normalizes omitted cache fields to zero and text-only output to zero reasoning;
the export cannot recover the original absence. The Responses probe supplies no
cache write count, and even supplied `output_tokens_details.reasoning_tokens=5` does
not reach the pinned reasoning export path. Entirely missing usage leaves all
three export details `NULL` for every spelling, even though base token columns
default to zero. No missing-path `NULL` should be interpreted as zero cache use.

### Agent Identity And Rejection Evidence

The projection also includes the pinned schema's `agent_id` and a SQL-derived
`rejection_kind`. Both remain present with `usage_details=false`. Agent identity,
like key/team/session identity, is sensitive: keep the export private. No raw
error messages or error metadata are exported.

The classifier recognizes only these LiteLLM v1.104.0 failure-writer shapes.
Every match requires `status=failure` and the `litellm_rate_limit` category,
plus the exact code, class, type and anchored message prefix in
[export.sql](export.sql):

| `rejection_kind` | Pinned failure shape |
| --- | --- |
| `session_budget` | 429, `ProxyRateLimitError`, budget type, session-budget prefix |
| `session_iterations` | 429, `ProxyRateLimitError`, max-iterations type, session-iterations prefix |
| `other_budget` | 422, `BudgetExceededError`, budget type, default budget prefix |
| SQL `NULL` | No recognized evidence; empty CSV field or JSONL `null` |

The default non-session budget error uses **422** in this pin even though its
normalized label mentions 429. The normalized label alone never classifies a
row. Provider rate limits, custom messages/status codes, conflicting types,
wrapped prefixes, absent metadata and successful rows remain unclassified.
The `other_budget` class is not evidence of a session-budget rejection.

Column presence establishes classification availability, not complete failure
logging. `NULL` does not prove that no rejection occurred. A recognized pre-call
rejection's zero usage does not establish a free successful model call. Export
one application's desired keys, teams and agents from one known proxy/counter
namespace for session-budget review; this file cannot authenticate that scope.

Legacy exports without these columns remain usable for existing comparisons.
Released v0.7.0 ignores both added columns; it does not interpret rejection
evidence or provide the new session-budget review. See the reproducible
[compatibility checks](TESTING.md#released-reader-compatibility).

### Claude Code And Codex Requests

The importer reads `anthropic_messages` / `aanthropic_messages` and `responses` /
`aresponses`, alongside Chat Completions and text completions. The pinned
LiteLLM logging lifecycle converts their native usage into the spend columns:

| Path | Native input | Cache read / write | Spend `prompt_tokens` | Output / total |
| --- | ---: | ---: | ---: | ---: |
| Anthropic Messages | 100 uncached | 80 / 20 | 200 | 10 / 210 |
| OpenAI Responses | 200 inclusive | 80 / absent | 200 | 10 / 210 |

These are checked synthetic values from the pinned image, not provider traffic.
The importer adds the spend columns once; cache details stay subsets. Responses
spend columns use the standard-logging fallback built by the logging lifecycle.
The [logging source](https://github.com/BerriAI/litellm/blob/79645770fedc7ec2627e6468d31062f20f82aecc/litellm/litellm_core_utils/litellm_logging.py)
and [writer probe](writer_probe.py) capture this contract.

If either selected period contains an unpinned call type, the first line says
**Comparison incomplete**. It lists each type's request counts and recorded
token columns as **unanalyzed**, separately from the supported totals. A period
containing only unsupported rows still yields this partial report when the other
period has analyzable requests. When neither period has any, the command names
the call types and asks for a supported request-level export.

## Data And Limits

- Accepted inputs are request-level SQL projections as headered CSV, a JSON
  array, or JSONL. LiteLLM Usage dashboard CSV/JSON exports are daily aggregates:
  the importer recognizes them and requests row-level data. API page envelopes
  and session-grouped views are unsupported; no API export recipe is included.
- fleetdiff reads a local file without a collector, database connection, model
  key, or provider call. Counts describe supplied logged model requests, not
  proven provider sends. Hidden retries and missing records cannot be recovered.
- Supported call types are Chat Completions, text completions, Anthropic Messages
  and Responses, including their asynchronous names. Other call types remain
  separate in the coverage inventory; unfamiliar names are locally aliased.
- Check your database's timestamp convention. The pinned schema stores UTC in
  `timestamp(3)` columns without a timezone; SQL emits explicit UTC RFC3339.
  Do not apply that interpretation to local-wall-time data. Requests belong to
  the half-open interval containing `startTime`. Retain `endTime`; absent end
  times withhold the per-request split.
- A date selects a UTC day. For two-period comparisons, explicit before/after
  RFC3339 `start/end` intervals must be equal-duration, ordered, disjoint, and
  completed before now. Without period flags, that comparison chooses two
  trailing seven-day UTC windows inside the file's interior days: ceiling of
  the earliest `startTime` through floor of the latest, capped at today. This
  heuristic does not certify coverage. The [single-period session-budget
  review](../../docs/SESSION_BUDGET.md) has separate period-selection semantics.
- Zero-only usage has unknown origin. Failures overlap usage categories and
  retain valid partial tokens. Unclear usage withholds the whole-period split;
  unchanged recorded totals do not prove unchanged real usage.
- Prompt and completion tokens are added once; `total_tokens` is a cross-check.
  Usage details are subsets, not extra totals or physical spend-log columns.
  Missing details stay null, invalid casts fail the SQL export, and arbitrary
  metadata is not accepted as proof of provenance. These paths are version-pinned.
- Recorded spend is source accounting, not an invoice. Default-zero cost is
  ambiguous. No provider pricing, run rate, or forecast is added.
- Output aliases hide raw identities, including models; displayed hashes are
  linkable. Use a private secret for real exports, never the fixed test secret.
  Sessions are scoped to keys in two-period comparisons. The
  [single-period session-budget review](../../docs/SESSION_BUDGET.md) groups by
  session ID alone within the supplied application/cache namespace. In either
  mode, session IDs may be generated trace IDs, not conversations.
- Tied contributors and models follow their first appearance in the file.
  Concentration uses the full tracked candidate set before `--top` truncates
  display rows. Its denominator is the net recorded increase, so offsets from
  declining contributors appear as added, offsetting and net token totals in
  text; JSON retains the corresponding share and its bounds.
- The SQL excludes raw error messages, prompts, responses, code, request bodies, IP addresses,
  API bases, and whole metadata objects. Exports and database diagnostics still
  contain sensitive information: keep them private. Use only successful exports;
  failed SQL can leave partial files. Retry into a new directory because `psql`
  can overwrite files. A consistent snapshot does not establish complete logging.
- Shared input limits: **10,000,000 rows; 16 GiB/file; 64 KiB/row; 8 KiB/field;
  64 fields/row; JSON depth 8.** Input limits also apply to ignored content.
  Two-period comparisons additionally limit context to **128 models and
  64 call types**. The single-period session-budget review instead has the
  [session and identity-membership limits](../../docs/SESSION_BUDGET.md)
  described in its guide. Select a narrower SQL interval when necessary.
  The recipe writes one selected format
  without sorting or aggregating rows and has a two-minute per-statement
  timeout; review large exports with your database operator.

## Synthetic Session-Budget Review

[session-budget.csv](session-budget.csv) is a newly authored, deterministic
Apache-2.0 fixture with four sessions and no production data. The companion
[license notice](session-budget.csv.license) keeps the CSV directly importable.
Use fleetdiff v0.8.0 for the session-budget command:

```sh
fleetdiff investigate --litellm-spend examples/litellm-spend/session-budget.csv \
  --session-budget-usd 1.00
```

The fixture contains 12 model-call rows and one classified rejection. Complete
recorded session totals are USD 0.20, 1.10, 1.20 and 1.60, with 1, 6, 2 and 3
model calls respectively. The USD 1.10 session has a subsequent budget
rejection; the USD 1.20 and 1.60 sessions have none recorded. The USD 1.60
session includes two keys, two teams and two agents over 4,000 seconds.

The first result is **3 of 4 sessions with complete recorded spend exceed the
supplied USD 1.00 cap.** Two above-cap sessions have no recorded session-budget
rejection. These are synthetic observation facts, not evidence of enforcement
effectiveness or complete failure logging.

[test_session_budget.py](test_session_budget.py) checks the fixture, exact
distributions, shared identities and privacy in text/JSON/errors, including
`--show-hashes`. Separate temporary variants exercise missing spend and old
exports without classification or agent columns; the base CSV stays unchanged.

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
For a default-period scale check, add `--days 16` and run the importer without
period flags. Increase `--rows` to `10000000` to exercise the supported row cap.
