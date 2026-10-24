# Provider count coverage

Compare local provider daily aggregate counts with LiteLLM logged rows without
changing recorded spend totals or making provider API calls.

```sh
./fleetdiff-install/fleetdiff investigate \
  --litellm-spend spend.csv \
  --provider-daily-counts provider.csv \
  --provider-model-map model-map.csv \
  --provider-counts-contract scope.json \
  --before-period 2026-09-01 --after-period 2026-09-02
```

Use these CSV headers and your own aggregate values:

```csv
date_utc,provider,provider_model,requests,input_tokens,output_tokens
2026-09-01,openai,gpt-6-luna,2,20,4
2026-09-02,openai,gpt-6-luna,3,30,6
```

The token columns are optional. An empty measurement is unavailable, not zero;
each row must supply at least one measurement. Dates, provider and model form
a unique row key. Requests use one primary counter; do not add errors and
throttles to requests. The map is optional for exact provider-qualified logged
IDs. For deployment aliases or unqualified model names, supply explicit rows:

```csv
source_provider,source_model,provider,provider_model
azure,my-deployment,openai,gpt-6-luna
```

An optional `scope.json` declares population and counting-unit comparability.
This is an illustrative contract, not evidence that any real export matches:

```json
{
  "schema": "fleetdiff-provider-counts/v1",
  "start_utc": "2026-09-01T00:00:00Z",
  "end_exclusive_utc": "2026-09-03T00:00:00Z",
  "retrieved_at_utc": "2026-09-04T00:00:00Z",
  "pagination_complete": true,
  "population": {
    "account": "matched",
    "project_or_workspace": "matched",
    "region_set": "matched",
    "gateway": "matched",
    "operation": "matched"
  },
  "count_basis": "primary_requests",
  "counting_units_match": true,
  "token_units_match": true,
  "settlement_margin_seconds": 86400
}
```

Only declare a match after verifying your retrieval filters, population and
units. Population fields accept `matched`, `unverified` or `mismatch`, not
account identifiers. Supported bases are `primary_requests`,
`bedrock_invocations`, `openai_model_requests` and `tokens_only`; token-only
contracts cannot claim matched request units. Provider-specific bases require
the corresponding provider. Keep actual retrieval evidence private.

The report distinguishes complete, partial and unsettled UTC days. Settlement
uses retrieval time, not the later time you run fleetdiff. Without a matched
population and request units, differences are arithmetic only. Even matched
aggregate counts identify a net row deficit, not individual missing requests
or their cause. Missing provider data and unmapped logged aliases remain visible.
Add `--litellm-daily-spend daily.csv` for day-level LiteLLM counters; the report
does not duplicate whole-day counts on model rows.

Aggregate inputs are bounded to 4 MiB, 10,000 rows and 2 KiB per physical CSV
line; multiline fields, duplicate/unknown headers and negative or noninteger
measurements are refused. The contract is limited to 8 KiB. This option is for
unscoped two-period LiteLLM investigation, not Langfuse, routing scopes or
session-budget commands. See [daily row coverage](ROW_COVERAGE.md) for the
separate LiteLLM SQL-export comparison.
