# Model-mix what-if

Re-price your recorded usage at another model's dated prices, tier by tier and
with caching, and see how sensitive the result is to output length. The first
line is Level 1: the same work and output length at the target prices. Benchmark
verbosity ratios appear separately as sensitivity, not a traffic prediction.

Install the [verified v0.15.0 binary](../README.md#1-install-v0150) before
running the commands below.

## Run it on your data

Use a request-level [LiteLLM SQL export](../examples/litellm-spend/README.md),
including input, output, cache-read and cache-write columns. Keep the source
and identity mapping files private. The command reads local files only.

```sh
./fleetdiff-install/fleetdiff what-if --litellm-spend spend.csv --period 2026-10-07 \
  --from claude-sonnet-5-5 --to gpt-6.1-sol --fraction 0.5
```

A period is one UTC date or an explicit half-open interval, for example
`2026-10-01T00:00:00Z/2026-10-08T00:00:00Z`. Select completed periods. A fraction
of `0.5` moves half the covered token volume, preserving its observed input,
output and cache composition. It does not select the largest or cheapest requests.

## What you'll see

Example output from the checked-in synthetic export:

```text
Moving 50% of claude-sonnet-5-5 traffic to gpt-6.1-sol (covered usage): $1.54 -> $1.49 at the same work and output length, on 2026-11-19 prices.
Target price tiers: tier 1: 3 requests at or under 272,000 prompt tokens.
```

The output-length sensitivity section gives QA, extraction and coding rows
using measured benchmark factors. Its output-share line shows how much of the
moved cost those factors can affect. Run a canary on your own traffic and use
`investigate`'s [session review](SESSION_BUDGET.md) to measure the actual
difference. One example row lacks cache details and is excluded, not treated
as free usage. Full checked output: [text](../examples/what-if/expected.text)
and [JSON](../examples/what-if/expected.json).

## No data handy? Try the sample

From a v0.15.0 checkout, using the verified binary installed above:

```sh
./fleetdiff-install/fleetdiff what-if --litellm-spend examples/what-if/spend.csv \
  --period 2026-10-07 --scenario examples/what-if/scenario.json
./fleetdiff-install/fleetdiff what-if --litellm-spend examples/what-if/spend.csv \
  --period 2026-10-07 --scenario examples/what-if/scenario.json \
  --workload-map examples/what-if/workload-map.json --format json
```

The [long-prompt sample](../examples/what-if/long-prompt.csv) has one call on
each side of Haiku 5.5's 100,000-token boundary. Earlier releases cannot run
its version 2 price table:

```sh
./fleetdiff-install/fleetdiff what-if --litellm-spend examples/what-if/long-prompt.csv \
  --period 2026-10-07 --from claude-sonnet-5-5 --to claude-haiku-5-5 \
  --fraction 1 --format json
```

The move's `target_tiers` lists one covered source request in each tier. These
counts precede the proposed move fraction; they are not a forecast of sent
requests.

## Scenarios and workload mappings

For several moves, use `--scenario` with a versioned file:

```json
{
  "version": 1,
  "moves": [
    {"from": "claude-sonnet-5-5", "to": "gpt-6.1-sol", "fraction": "0.25"},
    {"from": "claude-sonnet-5-5", "to": "claude-haiku-5-5", "fraction": "0.25"}
  ]
}
```

Each move can include `"scope": {"kind": "team", "value": "your-team-id"}`;
`key` is also supported. Simple moves accept `--scope-team` or `--scope-key`.
Use a protected file for identifiers that should stay out of shell history.
Selectors use the existing local keyed canonicalization and are never printed.
Moves apply to original traffic, not another move's output. Overlapping fractions
above 100% are rejected. If a scoped move needs a missing key/team identity,
that row is excluded as unknown eligibility, not treated as unchanged traffic.

`--workload-map` takes [this versioned shape](../examples/what-if/workload-map.json).
Use `qa`, `extraction` or `coding`. Conflicting key/team mappings are rejected.
Mapped records use their assigned workload; unmapped records are shown under
all three assumptions. Unmeasured pairs stay level 1 only, with
"no measured verbosity factor". No reverse or transitive factor is inferred.

## Prices and arithmetic

[Bundled prices](../internal/reprice/data/prices-v2.json) are dated USD per million
tokens at direct-provider standard/global rates. Each request selects a price
tier from its complete prompt length, including cache reads and writes; all
its input, cache and output tokens then use that tier. Make a copy with your
applicable rates and date, then pass `--prices prices.json`.
Every known source and target used in the report needs all four prices. Unknown
model IDs or missing prices are refused for a move; other unpriced input rows
are excluded and counted. The report labels overrides explicitly.
LiteLLM rows may use `provider/name`: the prefix must exactly equal that row's
`custom_llm_provider` and the exact catalog entry's provider. Use the public
catalog ID for `--from` and `--to`. No case folding, trimming or alias lookup is
performed; investigation labels use the same match.
Version 2 tables use ordered `tiers`, with `max_prompt_tokens: 0` meaning an
unbounded last tier. Each tier includes `input`, `output`, `cache_read` and a
`cache_write` map by duration. The model's `cache_write_ttl` chooses the
duration assumed for these records; the current reader does not observe a
per-request cache-write TTL. For example:

```json
{
  "version": 2,
  "date": "2026-11-19",
  "models": [{
    "id": "claude-haiku-5-5",
    "provider": "anthropic",
    "cache_write_ttl": "5m",
    "tiers": [
      {"max_prompt_tokens": 100000, "input": "0.10", "cache_read": "0.01", "cache_write": {"5m": "0.125", "1h": "0.20"}, "output": "0.50"},
      {"max_prompt_tokens": 0, "input": "0.50", "cache_read": "0.05", "cache_write": {"5m": "0.625", "1h": "1"}, "output": "2.50"}
    ]
  }]
}
```

Version 1 single-row tables remain readable with their prior output. A
replacement tier must be stated explicitly; an override cannot silently
extend a bundled model's priced range.

For inclusive input `I`, cache reads `R`, cache writes `W`, and billed output `O`:

```text
cost = ((I - R - W) * input_price + R * read_price
        + W * write_price + O * output_price) / 1,000,000
```

The subsets must be disjoint and within input. Reasoning is already included in
output and is not added again. Source and counterfactual costs use the same
dated table; these are not silently compared with discounted recorded spend.

If priced records include failure rows with tokens, the report shows their
recorded input/output and cache subsets, plus their contribution to each price
scenario. This subset is computed after model, scope, usage and price-tier
exclusions. The values are gateway-recorded, with finality unknown; they remain
included in the covered totals. The source failure count, when present, is
before pricing exclusions and is not the priced subset's count.

Level 1 keeps output unchanged, applies the measured pooled input factor across
providers (1.0 within a provider), and retains cache shares as an assumption.
Level 2 additionally applies the measured workload output factor. The factors
are geometric-mean paired ratios; their endpoints are approximate 95% intervals
for the log mean. They describe the uncertainty of the average factor, not a
range for your traffic. In a fresh calibration, they reliably contained measured
30-task batch costs only for Sol to Luna QA and extraction;
[see the calibration](WHAT_IF_CALIBRATION.md). Endpoint sums and the workload
envelope are sensitivity calculations.

## Collector summaries

Upgrade fleetdiff before enabling the collector's optional `model_usage` export.
Use the contract identifier calculated from the producer's approved public model
map, input conventions and limits:

```sh
./fleetdiff-install/fleetdiff what-if --summaries ./windows --expected app \
  --period 2026-10-07 --model-usage-contract "$MODEL_USAGE_CONTRACT" \
  --from claude-sonnet-5-5 --to gpt-6.1-sol --fraction 0.5
```

Only the joint cohort with recorded input, output, cache reads and cache writes
is priced. Missing, invalid and origin-unknown zero usage are counted separately;
reserved model buckets are never priced. Latest cumulative snapshots replace
earlier ones; replay never adds their totals twice. Conflicting contracts,
mixed enabled/disabled snapshots and regressions fail before a report is written.

The fixed input histogram retains request counts, token sums and per-request
extrema. A summary cohort can be priced only when its minimum and maximum
select the same source and target tier. A cohort spanning tiers needs
request-level rows; its average cannot select a tier. Fractions do not shrink
request sizes. Missing higher-tier rates still refuse the move.

Summaries have no model-by-key/team intersections, so scoped moves and workload
maps require request-level exports. Legacy summaries without `model_usage`
remain readable, but return null costs and a missing-split explanation. An
explicit expected model-usage contract requires the block to be present.

| Producer format | Reader | Result |
| --- | --- | --- |
| Legacy, block absent | Existing fleetdiff versions | Existing behavior |
| `model_usage` v1, `input-tiers-v1` | fleetdiff v0.10.0+ | Validated per-model repricing |
| Enabled block | fleetdiff v0.9.x and older | Rejected; upgrade the reader first |
| Unknown extension version or histogram | fleetdiff v0.10.0+ | Actionable upgrade error |

The shared format is available in sketchkit v0.4.0 and its identity-preserving
v0.2.4 maintenance backport. Fleetdiff uses v0.2.4 with its existing normalization
dependency; reading the extension does not migrate identity hashes. Collector
v0.3.3 does not produce this block. The producer upgrade is a separate opt-in.
The [synthetic summary example](../examples/what-if-summaries/README.md) includes
replaced and replayed snapshots, checked costs and each exclusion category.

## Evidence and limits

The [numeric evidence bundle](../evidence/model-mix/README.md) includes the paired
fit counts, dataset/license pins, exact model IDs, dates, estimator and price
sources. [Factors](../internal/reprice/data/factors.json) name that source record.
Reproduce them offline with `python3 scripts/reproduce-model-mix.py --check`.

- Factors are averages on public benchmark tasks; customer traffic may differ.
- Individual requests vary widely; the per-request range test failed.
- Repricing requires recorded input, output, cache-read and cache-write counts.
  Pinned LiteLLM v1.104.0 native Responses can retain positive reads and supplied
  writes; the bundled writer fixture supplies no write count and therefore
  lacks a complete split. Missing counts remain unknown, not zero.
- This re-pricing is not a forecast or an invoice.
- No quality equivalence is claimed.

The example and default-output golden tests run in CI, alongside the privacy
sentinels and network-disabled binary smoke test.
