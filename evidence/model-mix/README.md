# Measured Model-Mix Factors

Study date: **2026-10-14, America/New_York**. Paired token measurements for
descriptive, fixed-attempt model-cost comparisons.

## Files And Reproduction

[Fit counts](fit-token-counts.csv) contain 468 observations: 117 tasks on four
models. [Source records](source-record.json) bind public data hashes, dataset
pins, configurations and original estimates; [fit estimates](fit-estimates.json)
include all mean intervals. [Prediction summaries](prediction-audit.csv) retain
56 audited population results. Runtime [prices](../../internal/reprice/data/prices.json)
and [factors](../../internal/reprice/data/factors.json) are covered by the
[SHA-256 manifest](manifest.json).

From the repository root, with Python 3.11 or newer, no dependencies or network:

```sh
python3 -B scripts/reproduce-model-mix.py --check
python3 -B -m unittest discover -s evidence/model-mix -p 'test_*.py'
```

`--json` prints recomputed shipped factors without writing. Reproduction uses
only these public numeric records; no inference calls or network are needed.

## Original Fit

For each measured direction and workload, let `r_i = target_tokens/source_tokens`
on the same 39 fitting tasks. The original estimator is:

```text
mu = mean(log(r_i))
factor = exp(mu)
s = sample standard deviation of log(r_i), denominator n-1
low, high = exp(mu +/- 1.959963984540054 * s / sqrt(n))
```

This is a **geometric mean of paired ratios**, not an arithmetic mean or a ratio
of token sums. The approximate 95% interval estimates the population geometric
mean under the fit sampling assumptions, not individual-task or bill coverage.
It does not account for correlated tasks or production workload shift.

Arithmetic matches the frozen implementation: Decimal precision 50 for ratios,
logs, mean and exponentiation; float-converted logs with stdlib `statistics.stdev`
for sample SD; the float normal margin is converted through its decimal string.
Public factors round to 18 fractional digits using round-half-even, then remove
trailing zeros. Original full-precision values remain in `source-record.json`.
Zero or undefined ratios block a stratum rather than disappear from its sample.

## Common Input Factor

L1 uses a single factor per directed model pair without a workload map. Pool the
117 paired fit input counts in the order QA, extraction, coding, with 39 tasks
each, and apply the same geometric-mean estimator. This is a **new descriptive
pool**, not the original calibrated pooled envelope. It weights tasks equally,
not input tokens or production workloads.

Sonnet-to-Sol and Sonnet-to-Luna input factors are `0.668804954773279205` (`n=117`).
Within-provider input is fixed at `1`; both measured directions agree. Workload
input means vary from about 0.6535 to 0.6993; see `fit-estimates.json` for intervals.

The pool is a scenario assumption, not a universal tokenizer conversion or
conservative bound. Generalization needs representative paired measurements.

L2 applies **only the workload output factor after the common L1 input factor**.
Only listed measured directions are supplied; no inverse or transitive factors
are inferred, and workload input diagnostics are not applied again.

## Prices And Eligibility

Rates are frozen **Oct 14 published prices**, stored as literal decimal strings.

| Exact Requested Model ID | Input | Output | Cache Read | Cache Write | Known Input Tier Maximum |
| --- | ---: | ---: | ---: | ---: | ---: |
| `claude-sonnet-5-5` | 2 | 10 | 0.10 | 2.50 | No separate price ceiling recorded |
| `gpt-6.1-sol` | 2 | 10 | 0.10 | 2.50 | 272000 |
| `claude-haiku-5-5` | 0.10 | 0.50 | 0.01 | 0.125 | 100000 |
| `gpt-6-luna` | 0.10 | 0.50 | 0.01 | 0.125 | 272000 |

Units: **USD per million tokens**, direct-provider Standard/global, text-only,
short input, without discounts, premiums, taxes or account modifiers. Sonnet's
omitted price-tier ceiling does not imply unlimited context support.

Per-request inclusive source input and scaled target input must fit their known
tier maxima. Inclusive input includes cache reads and writes. Period summaries
cannot establish per-request tier eligibility.

Anthropic cache-write prices are for **5-minute** writes, not one-hour writes.
The frozen OpenAI assumption is **30-minute minimum retention**. Cache-hit
fractions and retention behavior are not predicted; all fit observations are cold.

Primary price/model references: [OpenAI pricing](https://developers.openai.com/api/docs/pricing),
[Sol](https://developers.openai.com/api/docs/models/gpt-6.1-sol),
[Luna](https://developers.openai.com/api/docs/models/gpt-6-luna),
[Claude pricing](https://platform.claude.com/docs/en/about-claude/pricing),
[Sonnet](https://platform.claude.com/docs/en/models/sonnet-5-5/overview), and
[Haiku](https://platform.claude.com/docs/en/models/haiku-5-5/overview).

## Population And Provenance

Dolly closed QA and extraction use revision
`bdd27f4d94b9c1f951818a7da7fd7aeea5dbff1a`; HumanEval Python uses revision
`6d43fb980f9fee3c892a914eda09951f772ad10d`. Dolly is CC-BY-SA-3.0 with Databricks
and Wikipedia attribution; HumanEval is MIT, copyright OpenAI. See
[license notices](LICENSES.md) and [source/hash pins](source-record.json).

The study selected 156 tasks per workload: 39 fit, 39 calibration, 78 held out.
Selection/grouping rules and seeds are in the source record. Requests use low
effort for QA/extraction and medium for coding, with a 4096-token output cap,
shared visible task text, and no tools. Output totals include billed reasoning.
Answer quality was not evaluated. Model IDs identify requested configurations;
exact returned snapshot IDs and fingerprints are unavailable.

The public fit allowlist is exactly
`workload,split_index,task_id,model,input_tokens,output_tokens`. IDs refer to public
dataset rows; no task text, answers, request bodies or account identifiers are
included. The complete study used two authorized recovery retries; the discarded
responses are unavailable, so the accepted logical cells are not an uninterrupted
no-retry sample.

## Validation Result

The original prediction study failed its per-task feature criteria:
**0/12 workload-specific and 0/12 pooled comparisons passed coverage and width
together**. The one six-task QA batch pass for Sol to Luna does not validate the
pooled default or monthly predictions. The public population summaries reproduce
these gate-count totals, not the underlying held-out predictions. The new input
pool has no held-out validation. These factors support descriptive scenarios;
the original calibrated-range claim remains unsupported.
