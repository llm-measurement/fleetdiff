# Agent Attribution

Development scope for the next minor release. The released v0.13.1 binary does
not support these options. Build this checkout to run the examples:

```sh
go build -o bin/fleetdiff ./cmd/fleetdiff
bin/fleetdiff investigate --litellm-spend examples/litellm-spend/agent-tags.csv \
  --before-period 2026-10-07 --after-period 2026-10-08 \
  --group-by agent --agent-source request-tag --agent-tag-prefix 'agent:' \
  --rank-by recorded-spend-change
```

The fixture records 620 tokens and $6.20 after the change: known agents account
for 500 tokens/$5, unknown identity for 50/$0.50, and ambiguous identity for
70/$0.70. `agents.csv` is the three-agent fixture without the last two rows.
Agent alpha's tokens increase from 100 to 300. These are synthetic observations.

## Source Selection

`--group-by agent` requires an explicit `--agent-source`:

- `agent_id`: the recorded LiteLLM spend-log ID, on CSV/JSON/JSONL or database.
- `request-tag`: request tags with a required `--agent-tag-prefix`. Supported on
  LiteLLM sources and Langfuse exports. The prefix is exact and case-sensitive;
  suffixes retain their source spelling. No fallback to another field occurs.

For LiteLLM database investigation, replace the file option with `--litellm-db`;
the selected tag source enables tag export automatically. Standalone native
export requires `--request-tags`; psql requires `-v request_tags=true`.
See [export limits](NATIVE_EXPORT.md#limits-and-failures) and the adjacent
[optional column grant](NATIVE_EXPORT.md#administrator-setup).

Zero matching nonblank suffixes means unknown identity. One distinct suffix
means a known agent, even if repeated. Two or more distinct suffixes means
ambiguous identity. Repeated matching tags and empty suffixes have separate
coverage counts; empty suffixes never match an agent. Unknown and ambiguous rows
retain all their accounting in separate buckets. Each analyzed row is counted
once. Langfuse identities remain project-scoped, and its unit stays GENERATION
observations. No request/observation join is inferred between sources.

## Ranking And Disclosure

The default agent table orders by signed recorded-token change.
`--rank-by recorded-spend-change` orders by signed change in usable recorded USD,
descending, with first appearance breaking ties. An unavailable cost delta sorts
after available deltas. Before/after USD, delta, cost coverage and each period's
share appear beside tokens. A period with no usable cost rows is unavailable,
not a recorded zero. Known agents outside `--top` are summed separately.

Shares use usable recorded USD in all analyzed rows in that period, including
unknown, ambiguous and undisplayed agents. Missing/invalid costs remain
unquantified; zero-cost origin remains unknown. Shares are rounded to 12 decimal
places in JSON and two percentage places in text. Costs retain exact source
decimals. Recorded USD is source accounting, not an invoice. Unsupported
LiteLLM call types and partial Langfuse exports keep their existing boundaries.

Default output uses keyed aliases. To include only the selected agent label,
add both `--show-labels --acknowledge-sensitive-labels`; either alone fails.
Reports identify disclosure and warn to review before sharing. Other tag values,
user/session/end-user/key identities and unrelated routing labels remain hidden.
Recorded IDs and tags can contain personal identifiers; no friendly-name lookup,
arbitrary metadata extraction or automatic agent discovery is performed.

## Limits

The agent table is exact and bounded to 1,024 known agents across both periods,
including project-scoped agents. Overflow fails and asks for a narrower prefix
or export; the error quotes the selected prefix. Malformed/non-string tag arrays
or tag data exceeding the existing 8 KiB field bound fail, without raw row data.
An old export without tags remains readable and reports column-absent unknown
identity. The optional `agents` JSON section is versioned independently as
`fleetdiff-agent-attribution/v1`; collector summaries and the base accounting
fingerprint are unchanged. Default export projections remain unchanged.
