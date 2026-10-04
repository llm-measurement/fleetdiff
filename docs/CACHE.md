<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Code authors: Vijay and Codex -->

# Cached-Token Share

Included in fleetdiff v0.5.0. Use the [verified release binary](OPERATIONS.md#install-and-verify)
or build from this source checkout.

`fleetdiff investigate` appends `cache` after the existing six questions:
**Did caching get worse?** No new sketch or collector configuration is required.

For each window, the recorded cached-token share is:

```text
cache_read_input_tokens / input_tokens
change in percentage points = 100 * (after share - before share)
```

Text describes this as **Share of recorded input tokens served from cache.**
The short explanation does not change the measurement or its evidence requirements.
JSON keeps the full explanation, including the distinction from request hit rate,
cost, savings, and provider billing.

A decrease means a smaller recorded fraction of input tokens was cache-read;
an increase means a larger fraction. Neither establishes the cause. The JSON
`cache` object retains exact before/after input and cache-read totals, fractional
shares, `delta_percentage_points`, and `direction` (`decreased`, `increased`, or
`unchanged`). Direction and change are computed with exact rational arithmetic
before conversion to floating point. Text rounds percentages to two decimals,
so tiny changes can display as zero; inspect JSON and integer totals for detail.

This is **not a request cache-hit rate, cost, savings, or provider billing
claim**. Input provenance may be unknown or inferred: a numeric field is not
necessarily provider-reported. Even provider-reported declarations do not
establish billing equivalence. A changed workload mix can change this share.

## Required Evidence

Both windows need complete declared observation intervals for every expected
producer and positive combined input-token totals. Partial observations remain
`cannot_determine` even with `--allow-partial`. Normal summary validation still
rejects invalid envelopes, overlaps, conflicting replays, and counter regression.

Every supplied snapshot must contain the typed integer counters `requests`,
`input_tokens`, and `cache_read_input_tokens`, plus all ten reviewed quality
counters below. For each of `input` and `cache_read_input`:

| Counter suffix in `token_observations.FIELD.STATE` | Required value |
| --- | --- |
| `reported` | Equal to that snapshot's `requests` |
| `missing` | Present and zero |
| `invalid` | Present and zero |
| `conflict` | Present and zero |
| `subset_violation` | Present and zero |

These are the collector's exported accounting states. Optional cache detail
does **not** increment `missing` when absent, so `missing == 0` alone is
insufficient. An invalid source can coexist with a reported value. A violating
individual detail can also be hidden by otherwise valid aggregate totals.
Accordingly, both reported coverage and every quality state are checked in each
original snapshot before trusting the combined totals. Opposing producer errors
cannot cancel. Each snapshot also requires cache-read tokens no greater than
input tokens, and no positive input total with zero model attempts.

An explicit zero cache total with complete quality evidence is a real zero.
Missing counters are unknown, never substituted with zero. Compatible older
snapshots lacking these quality counters produce `cannot_determine`; do not
reset their accounting fingerprint to manufacture compatibility. Different
counter schemas or accounting identities still fail the existing summary
compatibility checks, including mixed old/new schemas. This feature does not
normalize contracts or repair summaries.

Missing output usage, output-quality errors, and tool failures do not by
themselves block this answer. Its numerator and denominator use input only.
The overall usage-coverage or provider-origin question may therefore be limited
while the recorded cached-token share is observed. Duplicate and cumulative
snapshots retain the existing selection and merge semantics; only selected
snapshots contribute to totals, but all supplied snapshots must pass cache
quality checks.

Only the exact reviewed input/cache-read quality counter names are added to
report evidence. Unknown fields, states, and arbitrary counter names remain
omitted. Producer identifiers and metadata retain existing privacy behavior.
No raw prompts, user identifiers, or remote telemetry are needed.

## Offline Demo

From a source checkout with a local Go toolchain:

```sh
sh examples/cache.sh
sh examples/cache.sh --json
```

The script builds this checkout in a temporary directory. The build can download
Go dependencies, as the other source demos do; the resulting investigation runs
offline and makes no provider calls. With an already verified module cache and
toolchain, set `GOWORK=off GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off` for an offline
build too. Existing `GOCACHE` and `GOMODCACHE` settings are preserved.

The counter-only synthetic fixtures contain ten attempts and 1,000 input tokens
per window, starting at **2026-10-04 00:00 UTC** and **00:01 UTC**. Each window is
one minute long. Recorded cache-read tokens fall from 600 to 200: **60% to 20%, a
40-percentage-point decrease**. They contain no source-provenance declarations,
so provider origin correctly remains unknown. They are arithmetic fixtures,
not evidence of a real provider regression or savings.

When user and session rankings are absent, the text report ends with:

```text
Turn on user and session rankings (topk_keys) for more answers.
```

If only one ranking is missing, the hint names only that ranking. Configured
rankings with no recorded weight or incomplete snapshots are not presented as
disabled. Detailed coverage and provenance explanations remain in JSON.

Regenerate into a new directory and compare without overwriting the fixtures:

```sh
GOWORK=off GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local go run ./examples/cache -out /tmp/fleetdiff-cache-new
diff -ru examples/cache/data /tmp/fleetdiff-cache-new
go test ./internal/compare -run Cache
go test ./internal/cli ./examples/cache
```

Next, use complete real input/cache-read observations from the same accounting
contract. If quality is missing or conflicting, fix instrumentation first and
stop short of a cache-regression claim. For a valid decrease, inspect workload
and provider context separately before attributing cause or estimating money.
