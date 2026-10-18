# Which Models Served Each Route?

Available in v0.11.0: the optional routing audit compares the model distribution behind each
original LiteLLM route, with request, token and recorded-spend shares and explicit
attribution coverage. Classifier overhead is counted separately.

## Run It On Your Data

Use read-only access to the pinned LiteLLM PostgreSQL schema and a protected local
export directory. Add `-v routing_details=true` to the
[spend export recipe](../examples/litellm-spend/README.md). The default export is
unchanged. The additional columns are `original_model_group`,
`attempted_fallbacks` and `internal_call_origin`, from the tested LiteLLM 1.104.0
metadata paths. Keep the exported file private, like the existing key and agent
columns.

```sh
fleetdiff investigate --litellm-spend ./spend.csv \
  --before-period 2026-10-01 --after-period 2026-10-02 \
  --routing-audit
```

Add `--routing-by route-agent` to separate the known agents within each route.
Add `--show-labels` for a local report with the recorded route and agent names.
The usual `--group-by` still controls the existing contributor rankings; it does
not change the routing table. `--format json` includes the same evidence as an
additive `routing_audit` object. Default reports omit this object and section.

## What You'll See

Example output from the synthetic file below:

```text
  Model-request rows: 3 -> 3 requests; 360 -> 360 recorded tokens; recorded USD 0.0017 -> 0.0016.
    claude-sonnet-5-5: requests 2 -> 1; tokens 240 -> 120; recorded USD 0.0012 -> 0.0006.
      Cohort shares: requests 100% -> 50%; tokens 100% -> 50%; known recorded spend 100% -> 54.55%.
  Classifier routing overhead: 1 -> 1 rows; raw recorded total tokens 21 -> 21; recorded USD 0.00005 -> 0.00005.
```

The first route keeps two model requests in each period, but one of its requests
is served by another model in the second period. Its original model's request
share falls from 100% to 50%. The classifier row remains separate from those
requests. Recorded spend is the source's value, not a recomputed invoice.

## No Data Handy? Try The Sample

From a v0.11.0 checkout, use the [verified binary](../README.md#2-install-v0100):

```sh
./fleetdiff-install/fleetdiff investigate --litellm-spend examples/routing/synthetic.csv \
  --before-period 2026-10-01 --after-period 2026-10-02 --routing-audit
```

The fixture has eight synthetic rows across two UTC days, including two classifier
rows and Responses rows without agent attribution or cache-write detail. This is
a functional example, not a performance or savings measurement.

## Select A Route Or Agent

```sh
fleetdiff investigate --litellm-spend ./spend.csv \
  --before-period 2026-10-01 --after-period 2026-10-02 \
  --scope-route "$ORIGINAL_ROUTE" --routing-audit
```

Use one route or agent scope at a time. An investigation scope filters its rows;
the report also counts nonmatching known rows and rows whose identity is unknown.
Unknown rows are not evidence that traffic belongs to another agent or route.
Scopes require both explicit periods and cannot be combined with daily row
coverage or session grouping. A session-budget review cannot assess whole
sessions from a selected fragment.

For what-if, the same selector restricts the traffic moved, while retaining the
covered source baseline for other traffic:

```sh
fleetdiff what-if --litellm-spend ./spend.csv --period 2026-10-02 \
  --from claude-sonnet-5-5 --to gpt-6.1-sol --fraction 0.5 \
  --scope-route "$ORIGINAL_ROUTE"
```

Use `--scope-agent` instead for an agent. Scenario selectors and workload-map
selectors also accept `kind: "route"` or `kind: "agent"`, using the same
[versioned JSON structure](WHAT_IF.md) as keys and teams. Keep selectors in
protected configuration or environment variables rather than shell history.
An explicit workload map supplies the workload classification; label spelling
is never used to infer it. Summary files cannot supply route/agent intersections.

## Read The Evidence

Model IDs print when they exactly match the bundled public price catalog.
`provider/name` also matches when the prefix equals the same row's
`custom_llm_provider` and the exact catalog entry's provider. No aliases,
whitespace or case variants are resolved. This is also the what-if pricing rule;
other model and deployment names stay aliased, including under `--show-labels`.
The same public-model rule applies to `investigate`'s model totals. A catalog
match identifies a public name, not independent proof of the serving model.
Routes and agents stay aliased by default; `--show-hashes` adds keyed digests.
`--show-labels` adds their recorded labels beside the aliases (JSON: `label`),
with terminal controls escaped in text. Reports made with it contain
configuration labels: keep them local and review them before sharing.
Unknown identities and groups with conflicting raw spellings remain aliased.
Distinct source groups resolving to the same public model keep an alias suffix
so they remain distinguishable; their accounting groups and hashes are not merged.
Selector arguments are not echoed; errors never expose configuration labels.
An absent route stays unknown, even if `model_group` names a recovery route.
The original route comes only from `metadata.original_model_group`.

The model distribution uses the selected model and deployment fields together.
For example, a request rejected before dispatch can retain its requested model
string without a deployment ID; that is unknown serving identity. Request,
recorded-token and usable recorded-spend shares include unknown-model rows in
their denominators. Missing usage and spend remain visible alongside those
subtotals. A zero denominator has an unknown share, not a zero-percent claim.

Only `pass_through_endpoint` rows carrying the exact
`autorouter_classifier` origin count as classifier overhead. Other unsupported
rows remain separate. Neither category is relabelled as extra end-user requests.
Coverage by call type makes gaps such as missing Responses agents visible.

## Limits

The model basis is gateway-selected deployment, not independent provider-snapshot
confirmation or a final billing receipt. These are recorded values; failure-row
usage has unknown finality. An alias is a routing label, not a task class.

Different models behind one route demonstrate routing variation, not fallbacks.
The recorded fallback counter is shown only where present; it does not reconstruct
physical attempts. Requests omitted from the source cannot be recovered by this
audit. The audit reports distributions, not causal savings or quality equivalence.

Exact routing tables have fixed cardinality and input bounds. A limit produces a
narrowing error, not dropped rows. Display limits do not renormalize shares or
hide attribution coverage. New route/agent what-if scopes require selected-model
evidence and the existing complete token splits and exact model prices.
