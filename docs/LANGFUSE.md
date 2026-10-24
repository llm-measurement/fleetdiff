# Langfuse exports

Compare recorded Langfuse tokens and cost between two periods, find their
contributors, and check the supplied export's coverage.

Copy completed blob-storage export files and their completion manifests into a
private directory, preserving their relative object-key layout. Then run:

```sh
./fleetdiff-install/fleetdiff investigate \
  --langfuse-export "$HOME/langfuse-export" \
  --before-period 2026-10-07T00:00:00Z/2026-10-07T01:00:00Z \
  --after-period 2026-10-08T00:00:00Z/2026-10-08T01:00:00Z \
  --group-by provided_model_name
```

Use periods covered by your own manifests. Date-only periods select whole UTC
days; explicit start/end intervals select equal-duration half-open windows.
Without periods, the command uses the last two complete seven-day UTC periods.
It reads local files only, not Langfuse services or provider APIs.

## Try the synthetic example

Install using the [README](../README.md#1-install-v0130), then get the example
files (the installer does not include a source checkout):

```sh
git clone --depth 1 --branch v0.13.0 https://github.com/llm-measurement/fleetdiff
```

From the directory containing `fleetdiff-install` and this checkout, compare
the [included synthetic export](../examples/langfuse-spend/README.md):

```sh
./fleetdiff-install/fleetdiff investigate \
  --langfuse-export fleetdiff/examples/langfuse-spend/export \
  --before-period 2026-10-07T00:00:00Z/2026-10-07T01:00:00Z \
  --after-period 2026-10-08T00:00:00Z/2026-10-08T01:00:00Z \
  --group-by trace_name
```

The fixture increases from 8,000 to 16,000 recorded tokens and from USD 0.008 to
USD 0.016. Requests increase from four to six. The interactive workload grows
from 4,000 to 12,000 tokens while the background workload stays at 4,000, so one
tracked trace accounts for the entire net increase. Its private name appears
as an alias in the report. This is a change to investigate, not an explanation
of its cause or a predicted invoice.

Selected output from that command:

```text
Recorded tokens doubled (8,000 -> 16,000).
Logged model requests: 4 -> 6.
Who drove the increase?
  trace_name-1: 4,000 -> 12,000 tokens; change +8,000.
  trace_name-2: 4,000 -> 4,000 tokens; change +0.
  1 leading tracked trace_name accounts for 100% of the net recorded increase.
```

The v0.13.0 binary does not read a success or status field from Langfuse
observations: it reports unknown status and lists those records for usage review.
Recorded token and cost totals are still shown. The included fixture omits
`level`, so its records also remain in the review queue with the next-release
behavior below.

## Observation severity (unreleased)

The next release reads only the exact, case-sensitive `level` enum: `DEFAULT`,
`DEBUG`, `WARNING` and `ERROR`. Non-error severity does not prove a successful
provider call or complete billing. Call status remains unknown for all Langfuse
observations, and per-request volume splits remain unavailable.

`DEFAULT`, `DEBUG` and `WARNING` do not by themselves add a usage-review flag;
missing/invalid usage and other accounting checks still apply. `ERROR` observations
remain in the review queue and have a separate recorded-token subset, included
in the unchanged overall token and cost totals. These are observation errors,
not confirmed failed provider calls. JSON reports add `quality.observation_levels`,
`quality.error_observations` and `error_observation_tokens` on period/model windows;
`unknown_status` continues to count unknown call status, not severity.

Missing, null, empty, non-string, unrecognized or incorrectly cased levels are
counted as `unknown` and require review. No aliases, trimming or coercion are
applied. Only known enum strings (or the fixed `unknown` label) are retained;
arbitrary level text and all `status_message` values are discarded. Different
known levels, or known versus unknown, conflict for a duplicate observation ID;
equivalent JSON escaping is normalized. Different invalid values all project to
the same unknown level, not to successful status.

## Grouping and accounting

Choose `--group-by user_id`, `session_id`, `provided_model_name`, `environment`,
`release`, `trace_name` or `prompt_name`. For one named tag use `--group-by tag
--langfuse-tag TAG`. The default is `user_id`; when a minimal export omits that
field, choose a grouping it contains, such as `provided_model_name`. Add
`--format json` for automation. Private labels are keyed local aliases by
default; `--show-hashes` exposes linkable pseudonyms.

Only GENERATION observations enter model-call comparisons. Other types are
counted separately; listed scores files are verified and skipped. Native base
input/output and present disjoint cache/reasoning components are recomposed
once. Missing or invalid usage is not zero. Cost is the recorded usable
`total_cost`, not an invoice or proof of provider-origin completeness.

Matching accounting/grouping projections with the same project and observation
ID are deduplicated; conflicting copies are refused. Missing, rejected or extra
files and uncovered periods are reported. Observed totals can still be shown,
but incomplete copies do not support whole-period conclusions. Manifest checks
verify the local copy, not upstream ingestion or hidden provider retries.

## Limits

Use completed `observations_v2` JSON, JSONL or CSV exports, plain or single-member
gzip. The reader allows at most 1 MiB per manifest, 16 MiB stored or expanded per
file, 100,000 rows per file and 100,000 unique observations per directory. The
directory inventory is limited to 256 MiB, 4,096 entries and 1,000 manifests;
retained projected field text is limited to 64 MiB. Rows are limited to 64 KiB,
retained fields to 8 KiB, object fields/array items to 64, and nesting depth to
eight. A manifest lists at most 1,000 files.

Use an hourly schedule for a refused over-limit file; do not drop rows or raise
limits to make it pass. Symlinks, pipes, unsupported formats and changing local
copies are refused. Keep exports private. Observation `input`, `output`,
`metadata`, `tool_calls` and `status_message` values are not decoded or printed;
that reader property does not make the stored source export payload-free.
