# Fresh-Batch Calibration Records

The [calibration page](../WHAT_IF_CALIBRATION.md) reports all direction/workload
results. This bundle contains scalar measurements and public task identifiers.

## Reproduction

From the repository root with Python 3.11 or newer, stdlib only:

```sh
python3 -B docs/calibration/reproduce.py --check
python3 -B -m unittest discover -s docs/calibration -p 'test_*.py'
```

The first command must exactly match [expected-output.txt](expected-output.txt).
`--table` prints the page table and `--json` prints the complete analysis without
writing files. All commands are offline and perform no inference.

## Inventory

| File | Contents |
| --- | --- |
| [tasks.csv](tasks.csv) | Workload, batch, position, public task ID and normalized context/problem SHA-256 for every planned task. |
| [usage.csv](usage.csv) | One row per planned task/model cell, ordered by original sequence; token counts, priced cost, scalar validity flags, status and receipt liability fields. |
| [exclusions.csv](exclusions.csv) | Prior split, public task ID where applicable, and normalized group digest; synthetic pilot/cache entries carry only group digests. |
| [source-record.json](source-record.json) | Registered design, dataset revisions/licenses, frozen input hashes, dated price observation and stopped-cell identity. |
| [calculate.py](calculate.py) | Frozen Decimal/Fraction cost, coverage, width, Wilson and diagnostic calculations. |
| [reproduce.py](reproduce.py) | Strict CSV/JSON readers, exclusion checks, scalar replay, table rendering and exact-output verification. |
| [test_calibration.py](test_calibration.py) | Reproduction, arithmetic, missingness, documentation and privacy regression tests. |
| [expected-analysis.sha256](expected-analysis.sha256) | Digest of the complete public `--json` output, not an input to the calculation. |
| [expected-output.txt](expected-output.txt) | Expected `--check` stdout. |
| [manifest.json](manifest.json) | Exact publication file inventory and byte hashes. |
| [LICENSES.md](LICENSES.md) | Upstream attribution, source pins and changes made for this projection. |

Factors and prices are reused from the hash-pinned existing
[runtime data](../../internal/reprice/data/); no tool source or output changes
are required. Raw task text, reference answers, generated text, request/response
hashes, account identifiers and execution manifests are omitted.

## Scalar Contract

CSV column names and order are exact allowlists. Empty numeric/flag fields mean
unknown, not zero. `ok` denotes an accepted final receipt; `stop` retains a failed
finality check and its reservation; `missing` denotes an unattempted planned
cell. The exported status and identity are observations from receipt validation,
not a new validation of raw provider responses.

Input is inclusive: ordinary input plus cache-read and cache-write subsets.
Output already includes reasoning. Anthropic reasoning counts are unknown;
reported OpenAI reasoning is a subset of output. Prices are decimal strings
and must reconcile exactly with the frozen cold-text rates. The reservation and
settled-with-contingency fields reproduce receipt diagnostics only; they are
separate from token charges and do not reconstruct the budget journal.

`calculate.py` retains the original pure arithmetic functions. Monetary
arithmetic uses Decimal precision 80 with inexact operations trapped; relative
widths use exact Fraction comparisons and sorting, then 80-digit decimal output.
Wilson calculations use the registered normal quantile. Error medians use the
middle-pair arithmetic mean of observed errors; width ranks use all planned
batches. Unavailable errors remain null. A zero actual cost has zero relative
width only when the endpoint span is also zero, otherwise infinite width.

The expected digest was derived from the original analysis after removing only
provenance hashes unavailable in this public projection. Every numeric value,
numeric decimal string, boolean and null is retained, including cell diagnostics,
secondary envelopes and full-scenario displays. A digest mismatch fails before
table verification; matching rounded values alone is insufficient. The expected
digest and stdout are never read by `--json` or the arithmetic.

## Exclusion Check

The prior inventory covers 468 ordinary tasks (fit, calibration and evaluation),
2 synthetic pilots and 13 cache groups. Fresh tasks and prior exclusions join
on exact public IDs and SHA-256 of UTF-8 `" ".join(text.split())`: Dolly `context`,
MBPP problem `text`, and the prior HumanEval problem/pilot/cache text as recorded.
No case folding or semantic similarity is used. The checks prove zero ID/group
overlap and no repeated selected group, including across Dolly and MBPP.

Within each workload, the selected tasks are ordered by SHA-256 of
`fleetdiff-aggregate-v1-20261015|workload|context_sha256`, ties broken by public
task ID, then divided into consecutive batches. The test recomputes that order.
Source eligibility retained the first source-order byte-eligible representative
of each group before historical exclusions and selection. The public projection
verifies the retained selection/exclusion joins; it does not redistribute the
full candidate corpus or serialized requests to reconstruct eligibility.

Dolly IDs are one-based original JSONL line numbers; MBPP IDs are the original
integer `task_id` from full/test. Dataset file and card pins are in
[source-record.json](source-record.json). Historical HumanEval identifiers use
their original `HumanEval/number` form. Dataset text and solution/test fields
are not needed to rerun the measured cost analysis.
