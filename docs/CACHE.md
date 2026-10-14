<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Code authors: Vijay and Codex -->

# Compare Recorded Cache Reuse

Compare cache-read and cache-write shares before and after a change, then find
the keys, teams or sessions whose recorded uncached input grew. The expanded
view is available in **v0.7.0**. Versions v0.5.0 through v0.6.1 already provide
aggregate cache-read-share comparison for summary windows.

## Try The Cache Drop

Build this checkout, then run the deterministic four-request example:

```sh
go build -o ./fleetdiff-cache ./cmd/fleetdiff
./fleetdiff-cache investigate --litellm-spend examples/litellm-spend/cache-drop.csv \
  --before-period 2026-10-07 --after-period 2026-10-08 --group-by key
```

Selected output:

```text
But cache reuse fell: cache-read share 92% -> 61% (uncached input +620).
    Cache-read share of recorded input: 92% -> 61%.
      Cache reads: 1,840 of 2,000 input tokens -> 1,220 of 2,000 (all requests report cache detail).
    Cache-write share of recorded input: 5% -> 7.5%.
      Cache writes: 100 of 2,000 input tokens -> 150 of 2,000 (all requests report cache detail).
    Uncached input +620: cache-read share fell from 92% to 61%.
  key-1: 80 -> 700 uncached input tokens; change +620.
```

Total input and output stay flat. One synthetic key accounts for the added
uncached input. Use `--group-by team`, `user`, `end-user` or `session` for another
view. Aliases refer to the same identities across the total-token and cache
rankings in one report; `--show-hashes` also prints their keyed hashes.
Sessions remain scoped to their API key.

## Measurements And Coverage

The same quantities and wording apply to spend files and summary windows:

| Measurement | Calculation on the same covered requests |
| --- | --- |
| Cache-read share of recorded input | Sum of cache-read tokens / sum of input tokens |
| Cache-write share of recorded input | Sum of cache-write tokens / sum of input tokens |
| Uncached input tokens | Input tokens minus cache-read tokens, including cache writes |

LiteLLM exports get a breakdown per model. The [SQL export](../examples/litellm-spend/export.sql)
includes cache and reasoning fields by default, with `usage_details=false` to
omit them. Paths and normalization are pinned to the tested LiteLLM v1.104.0
writer; consult the [verified path table](../examples/litellm-spend/README.md).
Normalized `prompt_tokens` already includes cache reads and writes on the tested
Messages path. Those details are subsets and are never added a second time.

Each field has its own covered-input denominator and request count. Missing,
invalid or subset-violating details are excluded and counted separately. A read
can remain usable when the write field is absent, and conversely. Explicit zero
detail is a recorded zero; a missing detail or zero denominator has a null share.
The report counts recorded zero details with unknown origin separately: the
pinned Messages writer can fill absent detail with zero before export.
If neither period contains cache detail, text prints one line:
`Cache detail not recorded in this export.`

The uncached-growth ranking composes the existing keyed frequent-items sketches
and their integer bounds. It uses read-covered requests with a group identity.
Candidates are ordered by the lower bound of their increase; uncertain positive
growth remains a range. Missing cache detail and missing identities stay outside
ranking weights and have separate coverage counts. Both periods need at least
one read-covered request before a growth comparison is available.

A whole-model `Uncached input +X` explanation appears when both periods have
complete read coverage, read share fell, and uncached input grew by at least half
the net token increase (or total tokens stayed flat or fell). This is arithmetic
visibility. Increasing input volume or a changed workload mix can also increase
uncached tokens; the ranking alone does not establish a cache-share loss per key.

With complete cache-read coverage, a second headline highlights a change of at
least five percentage points, or an uncached-input change of at least 100 tokens
and 20% relative to the before period. From zero uncached input, the 100-token
minimum applies. The comparison uses exact token ratios before formatting.
When cache-read share falls, the next step points to the provider's cache
diagnostics and the gateway route's preservation of cache markers.

## Collector Summary Windows

```sh
sh examples/cache.sh
sh examples/cache.sh --json
```

The released collector exports aggregate read/write totals and per-field
observation-quality counters. Summary inputs must pass existing compatibility,
replay, interval and producer-coverage checks. Each original snapshot must report
input and the relevant cache field on every attempt, with zero missing, invalid,
conflict and subset-violation counters. Missing optional fields do not increment
the collector's missing counter, so reported coverage is also required.

Read and write are validated independently. If partial cache detail exists,
summaries cannot recover the matching input denominator: that share stays null.
All supplied snapshots are checked for quality; only selected snapshots enter
totals. This prevents opposing producer errors or superseded snapshots from
hiding invalid accounting.
When summary counters cannot establish distinct excluded-request counts, those
counts stay null; the original overlapping quality counters remain in JSON.

**Current data gap:** these summaries contain neither per-model cache counters
nor cache-weighted key/session rankings. The report names the gap rather than
inferring cache use from total-token rankings. No collector or wire-format
change is needed for the aggregate view.

JSON adds `cache_view` to the summary cache question and `cache` to spend windows,
models and reports, with explicit null quantities for unknown observations. The
existing summary `cache` result remains available when its original full-coverage
requirements hold, including exact-rational direction and percentage-point change.

## Limits

These are recorded token shares, not request hit rates or cause diagnoses.
Usage origin can remain unknown, including gateway-filled values. Recorded spend
continues to be summed as supplied; cache loss gets no price or savings estimate.
Ranking bounds describe tracked contributors within the covered population.
Percentages are rounded for text; JSON retains integer totals and bounds.
Synthetic examples demonstrate arithmetic, not provider cache behavior.

## Verification

```sh
go test ./internal/spend ./internal/compare ./internal/cli -run Cache
go test ./examples/cache
```

The pinned-image writer/DB checks and privacy sweeps are documented in
[LiteLLM testing](../examples/litellm-spend/TESTING.md).
