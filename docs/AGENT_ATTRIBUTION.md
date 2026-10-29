# Agent Spend Attribution

Find which recorded agents drove a token or spend change between two periods
of LiteLLM spend logs or Langfuse exports.

The included synthetic fixture goes from **300 to 620 tokens** and **USD 3 to
6.20**. Alpha adds **200 of the 320-token net increase (62.5%)**. After the
change, known agents account for 500 tokens/USD5, unknown identity for
50 tokens/USD 0.50, and ambiguous identity for 70 tokens/USD 0.70.

## Run The Fixture

These options are prepared for v0.14.0, not available in v0.13.1. Build this
checkout while release publication is pending:

```sh
go build -o bin/fleetdiff ./cmd/fleetdiff
bin/fleetdiff investigate --litellm-spend examples/litellm-spend/agent-tags.csv \
  --before-period 2026-10-07 --after-period 2026-10-08 \
  --group-by agent --agent-source request-tag --agent-tag-prefix 'agent:' \
  --rank-by recorded-spend-change
```

The answer includes:

```text
agent-1 accounts for 62.5% of the net recorded increase (+200 of +320 tokens).
```

The table shows before/after tokens, recorded USD, USD change and each period's
spend share. Unknown and ambiguous changes appear separately below the ranking.
The four cost-coverage columns appear only when a displayed row has missing,
invalid or zero-origin-unknown cost; otherwise one line confirms usable recorded
USD in both periods. Known agents outside `--top` remain in a separate total.
Agents whose USD change is unavailable are always listed, even with `--top`.

## Choose The Source

- LiteLLM recorded IDs: `--group-by agent --agent-source agent_id`.
- LiteLLM or Langfuse tags: `--group-by agent --agent-source request-tag
  --agent-tag-prefix 'agent:'`.

The default ranking is signed recorded-token change. Add
`--rank-by recorded-spend-change` for signed recorded-USD change, descending;
first appearance breaks ties.

For a LiteLLM database, replace the file option with `--litellm-db`. Tag
attribution enables the required tag projection automatically. Standalone
native export needs `--request-tags`; psql needs `-v request_tags=true`.
Exporting tags retains all tag values, not just the selected prefix. Review
the [export limits](NATIVE_EXPORT.md#limits-and-failures) and
[optional column grant](NATIVE_EXPORT.md#administrator-setup) before sharing an
export. These transport options do not disclose unrelated tags in reports.

## Include Selected Labels

Add both `--show-labels --acknowledge-sensitive-labels`; either alone fails.
The selected label is escaped in the answer and table, for example:

```text
agent-1 "alpha" accounts for 62.5% of the net recorded increase (+200 of +320 tokens).
```

Langfuse identities stay project-scoped. Identical labels are distinguished as
`agent-1 "alpha" (project-1)` and `agent-2 "alpha" (project-2)`, never with raw
project names. Project aliases describe this report's encounter order, not a
cross-report project ID. Unrelated tags and user/session/end-user/key identities
remain hidden; the text warns before sharing disclosed labels.

## Limits

- The selector is explicit: no field fallback, agent discovery, friendly-name
  lookup or request/observation join. Langfuse counts GENERATION observations.
- Prefixes are exact and case-sensitive. Zero distinct nonblank suffixes means
  unknown, one means known, two or more means ambiguous. Repeated identical tags
  are deduplicated; repetitions and empty suffixes have separate coverage.
  Each row contributes once, and all identity buckets retain their accounting.
- LiteLLM arrays with any non-string element remain unknown with
  `non_string_tag_rows` coverage; no string label is salvaged. Langfuse retains
  string-only arrays. Missing source columns remain column-absent unknown.
- Malformed JSON, non-array tags, invalid UTF-8 and source fields over 8 KiB fail
  without dumping rows. More than 1,024 project-scoped known agents fails and
  asks for a narrower prefix or export; identities are not silently dropped.
- Shares use usable recorded USD from all analyzed rows, including unknown,
  ambiguous and undisplayed agents. Missing/invalid costs stay unquantified;
  zero-cost origin stays unknown. A period with no usable cost is unavailable,
  not zero. Costs retain source decimals; JSON shares use 12 decimal places and
  text percentages use two. Recorded USD is source accounting, not an invoice.
- The optional JSON section remains `fleetdiff-agent-attribution/v1`. Its
  fields, base accounting fingerprint, default export projection and reports
  without agent mode are unchanged. Project display aliases are text-only.
  Unsupported LiteLLM call types and partial Langfuse exports retain their
  existing boundaries.
