# What would switching models have cost?

Re-price a period of recorded usage under a proposed model mix, with the price
date, measured factors and covered observations shown beside the result.

Available in **v0.10.0**. Install the [verified binary](../README.md#2-install-v0100)
before running the commands below.

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
Moving 50% of claude-sonnet-5-5 traffic to gpt-6.1-sol (covered usage): $0.154400 -> $0.109398 to $0.147974, depending on workload assumptions.
```

The report gives level 1 at unchanged output verbosity, then QA, extraction and
coding results with their measured factor intervals. It identifies how much of
the moved traffic's target cost is output: that is the portion affected by the
verbosity factor. One example row lacks cache details and is excluded, not
treated as free usage. Full checked output: [text](../examples/what-if/expected.text)
and [JSON](../examples/what-if/expected.json).

## No data handy? Try the sample

From a v0.10.0 checkout, using the verified binary installed above:

```sh
./fleetdiff-install/fleetdiff what-if --litellm-spend examples/what-if/spend.csv \
  --period 2026-10-07 --scenario examples/what-if/scenario.json
./fleetdiff-install/fleetdiff what-if --litellm-spend examples/what-if/spend.csv \
  --period 2026-10-07 --scenario examples/what-if/scenario.json \
  --workload-map examples/what-if/workload-map.json --format json
```

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

[Bundled prices](../internal/reprice/data/prices.json) are dated USD per million
tokens, for the recorded standard/global tier and cache-write duration. Make a
copy with your applicable rates and date, then pass `--prices prices.json`.
Every known source and target used in the report needs all four prices. Unknown
model IDs or missing prices are refused for a move; other unpriced input rows
are excluded and counted. The report labels overrides explicitly.
Omitted input-tier limits and cache-write durations retain the bundled values;
an applicable replacement tier must be stated explicitly in the price file.

For inclusive input `I`, cache reads `R`, cache writes `W`, and billed output `O`:

```text
cost = ((I - R - W) * input_price + R * read_price
        + W * write_price + O * output_price) / 1,000,000
```

The subsets must be disjoint and within input. Reasoning is already included in
output and is not added again. Source and counterfactual costs use the same
dated table; these are not silently compared with discounted recorded spend.

Level 1 keeps output unchanged, applies the measured pooled input factor across
providers (1.0 within a provider), and retains cache shares as an assumption.
Level 2 additionally applies the measured workload output factor. The study used
**geometric mean paired ratios**, with approximate 95% intervals for the log mean,
not arithmetic-mean token conversion or request prediction intervals. Endpoint
sums and the workload envelope are sensitivity calculations, not a joint 95%
confidence statement. Nothing shrinks by the number of customer requests.

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
extrema. The current price format has one applicable input tier per model.
Every request in a priced cohort must fit it: source cohorts crossing the tier
are excluded, and a move crossing the target tier is refused. Fractions do not
shrink request sizes. No average-based tier selection or higher-tier rate is
invented.

Summaries have no model-by-key/team intersections, so scoped moves and workload
maps require request-level exports. Legacy summaries without `model_usage`
remain readable, but return null costs and a missing-split explanation. An
explicit expected model-usage contract requires the block to be present.

| Producer format | Reader | Result |
| --- | --- | --- |
| Legacy, block absent | Existing fleetdiff versions | Existing behavior |
| `model_usage` v1, `input-tiers-v1` | fleetdiff v0.10.0+ | Validated per-model repricing |
| Enabled block | fleetdiff v0.9.x and older | Rejected; upgrade the reader first |
| Unknown extension version or histogram | fleetdiff v0.10.0 | Actionable upgrade error |

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
- This re-pricing is not a forecast or an invoice.
- No quality equivalence is claimed.

The example and default-output golden tests run in CI, alongside the privacy
sentinels and network-disabled binary smoke test.
