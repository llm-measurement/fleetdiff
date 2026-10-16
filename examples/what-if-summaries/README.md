# Reprice Synthetic Model-Usage Summaries

This local example moves half the covered `claude-sonnet-5-5` usage to
`gpt-6.1-sol`. It makes no collector, database, or provider calls.

From the repository root:

```sh
contract=$(cat examples/what-if-summaries/snapshots/contract.txt)
./fleetdiff-install/fleetdiff what-if \
  --summaries examples/what-if-summaries/snapshots \
  --period 2026-10-07T00:00:00Z/2026-10-07T00:01:00Z \
  --expected demo-a,demo-b \
  --model-usage-contract "$contract" \
  --from claude-sonnet-5-5 --to gpt-6.1-sol --fraction 0.5 \
  --format json
```

The expected contract for these synthetic files is:

```text
model-usage-v1:26bee4d603832d8c6e63bc4b77354edab553c54202fac473a755dc348123dfe3
```

`main.go` computes this ID with sketchkit's `ContractID`, using the synthetic
accounting ID `synthetic-model-usage-v1`, this approved demo mapping, and the
resolved routing configuration below. It is not a contract for production data.

| Observed Demo Name | Public Model ID |
| --- | --- |
| `demo-sonnet` | `claude-sonnet-5-5` |
| `demo-sol` | `gpt-6.1-sol` |

Routing uses `inactive-lru-v1`, `max_label_bytes=256`, `max_slices=16`, and one
selected slice named `model`. Its ordered keys and resource fallback keys are
both exactly `["gen_ai.request.model"]`. The unmapped demo name is classified
as `__unmapped__`, never passed through as a public model ID.

## Expected Accounting

The [verified v0.10.0 binary](../../README.md#2-install-v0100) reports:

```text
Moving 50% of claude-sonnet-5-5 traffic to gpt-6.1-sol (covered usage): $0.798000 -> $0.659080 to $0.675154, depending on workload assumptions.
```

Full checked output: [text](expected.text), [JSON](expected.json).

| Observation | Count |
| --- | ---: |
| Deduplicated attempts | 9 |
| Complete, known-model attempts | 5 |
| Missing cache split | 1 |
| Invalid cache partition | 1 |
| Zero usage with unknown origin | 1 |
| Unpriced reserved bucket | 1 |

`a-latest.json` replaces the partial cumulative `a-old.json`.
`a-replay.json` is an identical replay of the latest snapshot and adds nothing.
`b.json` belongs to the second disjoint producer. Both producers cover the full
selected minute after replacement.

Four complete source-model attempts contain 500,000 input tokens in total.
Their second histogram bin contains 310,000 input tokens across two requests,
with observed minimum 150,000 and maximum 160,000. Tier checks must use those
per-request extrema, not the bin total or its average. The fifth complete
attempt is existing `gpt-6.1-sol` usage and stays unchanged by the move.

Source and target tier checks retain price-file v1's single-band limits.
Missing components are excluded, not filled with zero. Key/team scopes and
workload maps are unavailable because the summaries contain no intersections.
This synthetic fixture supplies accounting examples, not new pricing evidence.

## Reproduce The Files

The generator requires a new output directory and prints the computed contract:

```sh
work=$(mktemp -d)
go run ./examples/what-if-summaries --out "$work/summaries"
diff -r examples/what-if-summaries/snapshots "$work/summaries"
go test ./examples/what-if-summaries
```

The four JSON files are canonical, with no trailing newline. `contract.txt` is
a convenience file, not a summary, and is ignored by the summary directory reader.
