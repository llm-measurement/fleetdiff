# Two of twelve batch-cost cases passed

The fresh-batch check passed for Sol to Luna QA and extraction; the other
directions and coding cases failed the registered coverage-and-width gates.

<!-- calibration-table:start -->
| Direction | Workload | Covered / 20 | Wilson 95% | Width rank 10 / 18 | Median signed error USD | Median absolute error USD | Result |
| --- | --- | ---: | --- | --- | ---: | ---: | --- |
| Sonnet -> Sol | QA | 7/20 | 18.1%-56.7% | 15.6% / 16.8% | +0.00127615 | 0.00127615 | Fail |
| Sonnet -> Sol | Extraction | 12/20 | 38.7%-78.1% | 14.0% / 16.3% | -0.00085064 | 0.00095675 | Fail |
| Sonnet -> Sol | Coding | 0/20 | 0.0%-16.1% | 19.6% / inf | -0.01074997 | 0.01074997 | Fail; incomplete |
| Sonnet -> Haiku | QA | 11/20 | 34.2%-74.2% | 26.4% / 29.9% | -0.00017191 | 0.00017191 | Fail |
| Sonnet -> Haiku | Extraction | 2/20 | 2.8%-30.1% | 22.5% / 24.3% | -0.00035491 | 0.00035491 | Fail |
| Sonnet -> Haiku | Coding | 0/20 | 0.0%-16.1% | 102.2% / inf | +0.00088186 | 0.00088186 | Fail; incomplete |
| Sonnet -> Luna | QA | 14/20 | 48.1%-85.5% | 21.8% / 25.2% | +0.00003268 | 0.00003895 | Fail |
| Sonnet -> Luna | Extraction | 13/20 | 43.3%-81.9% | 24.3% / 29.8% | -0.00007529 | 0.00010729 | Fail |
| Sonnet -> Luna | Coding | 3/20 | 5.2%-36.0% | 38.8% / inf | -0.00082431 | 0.00082431 | Fail; incomplete |
| Sol -> Luna | QA | 19/20 | 76.4%-99.1% | 18.4% / 21.4% | -0.00002027 | 0.00003031 | Pass |
| Sol -> Luna | Extraction | 17/20 | 64.0%-94.8% | 27.6% / 30.9% | -0.00000580 | 0.00004898 | Pass |
| Sol -> Luna | Coding | 1/20 | 0.9%-23.6% | 46.6% / inf | +0.00086237 | 0.00086237 | Fail; incomplete |
<!-- calibration-table:end -->

Coding is incomplete: each direction has 10 available and 10 unavailable
batches. Unavailable batches count as coverage failures with infinite width
(`inf`), so coding rank 10 is the largest observed width and rank 18 is infinite.
Signed error is the point estimate minus measured cost; the error medians use
observed batches only: 20 for QA/extraction and 10 for coding.

## Method

The design, batch-size amendment, dataset eligibility and exclusions were
registered before the run, on October 15, 2026, with the earlier results already
known. It ran once on October 17, 2026, on one machine. Standard/global prices
were checked against the official provider pages that day and matched the frozen
rates; the [source record](calibration/source-record.json) preserves that dated
observation and the exact model IDs.

The frozen v0.10.0 inputs were used without refitting:

| Input | SHA-256 |
| --- | --- |
| [Factors](../internal/reprice/data/factors.json) | `cd1b087e294a3d9768cc52b8dd8a6d0b2ab169bcb8b979bc44957310869d2b99` |
| [Prices](../internal/reprice/data/prices.json) | `5d0e1af2d161af4823e92f00123b3b724b5b7f528d5984cfcc05242e189d252f` |
| Frozen calculator snapshot | `6550dda842db930bcad592518b19f8a923564e7ed8f531af916312cccb547372` |

The approved calculator snapshot differed from released validation/tier handling;
its cost arithmetic, movement fraction and factor endpoints were unchanged, and
these short, cold-text batches did not cross the changed tier boundary.

There were 20 fresh batches per workload: 30 tasks each for Dolly closed QA and
extraction, and 24 each for MBPP full/test coding. Four model measurements per
task support all four directions. Dolly revision
`bdd27f4d94b9c1f951818a7da7fd7aeea5dbff1a` is CC-BY-SA-3.0, attributed to
Databricks, Inc. and Wikipedia editors and contributors; MBPP revision
`4bb6404fdc6cacfda99d4ac4205087b89d32030c` is CC-BY-4.0, attributed to Google
Research and Austin et al. See [attribution and license notices](calibration/LICENSES.md).
Earlier pilot, fit, calibration, evaluation and cache tasks and matching
whitespace-normalized context/problem groups were excluded; the
[public IDs and exclusion proof](calibration/README.md#exclusion-check) are checked offline.

The target is the cold-text cost of the moved half, using a movement fraction of
0.5. Level 1 applies the frozen pooled input factor; Level 2 additionally applies
the frozen workload output factor. The unchanged half appears only in the
full-scenario display. Relative width is `(upper - lower) / measured moved cost`.
Each case must cover at least 16/20 batches, with width at rank 10 at most 50%
and rank 18 at most 100%. The two-sided 95% Wilson display and width ranks use
all 20 planned batches. The secondary pooled envelope passes no case under all
gates and cannot rescue a primary failure.

The run stopped when a coding receipt failed final-receipt validation
(`receipt_finality`, Sonnet on `mbpp-383`). There were no retries, replacements
or continuation. The retained status establishes a validation failure; its
underlying provider stop reason is unavailable.

## What The Numbers Mean

The endpoints are approximate intervals for the average verbosity factor,
specifically the geometric mean estimated through the log mean, not prediction
intervals for a batch or a request. The two passing cases support only the
tested Sol-to-Luna, cold-text, 30-task Dolly QA and extraction comparisons, not
other directions, workloads or future traffic. The coding factors fitted on
HumanEval did not transfer reliably to the observed MBPP batches; the stopped
run also leaves half of coding unavailable. The earlier per-request range claim
remains failed. No answer-quality evaluation or quality equivalence is implied.

## Reproduce

From the repository root, with Python 3.11 or newer and no dependencies, network,
credentials or model calls:

```sh
python3 -B docs/calibration/reproduce.py --check
python3 -B -m unittest discover -s docs/calibration -p 'test_*.py'
```

[Expected output](calibration/expected-output.txt) binds the complete unrounded
analysis, including every cell, batch, diagnostic and secondary result.
`--table` prints the regenerated table; `--json` prints the full analysis.
See the [scalar-data inventory](calibration/README.md) for formats and checks.
