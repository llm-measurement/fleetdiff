# Frequently Asked Questions

## Where Do The Summary Files Come From?

The current input is canonical `llm-sketchkit` summary v1 JSON. It contains counters,
mergeable sketch state, and metadata describing the observation window and producer.
It is not a raw OTLP trace, a Prometheus scrape, or a top-k log line.

- For OpenTelemetry pipelines, enable the
  [summary exporter](https://github.com/llm-measurement/otelcol-genai-sketches/blob/main/docs/SUMMARY_EXCHANGE.md)
  in `otelcol-genai-sketches 0.1.0` or a later compatible version.
- For custom Go or Python pipelines, use the `llm-sketchkit 0.2.0`
  [summary exchange API](https://github.com/llm-measurement/llm-sketchkit/tree/v0.2.0/examples/summary-exchange).

The [comparison contract](COMPARISON.md) lists supported measurement names. Unknown
measurements are validated but omitted from the report, with an omitted count.

## Why Summaries Instead Of Raw Traces?

Different teams can retain their own traces and share measurements for a common
investigation. fleetdiff does not need access to every underlying prompt or user ID.

Underneath, `llm-sketchkit` provides bounded sketch state for distinct counts and
weighted frequent items. That keeps the state from growing with every new identity.
The purpose here is to compare activity across systems; the sketches are how the
comparison avoids requiring everyone's raw events.

If exact records are safe to share and straightforward to query, use them. This
tool does not replace an archive, trace explorer, or billing ledger.

## Can I Combine Hosted And Self-Hosted Systems?

Yes, when their exports describe compatible measurements. Hosting location is not
the deciding factor. Producers must agree on scope, accounting, window duration,
sketch settings, and hashing keys. Shared identities can then merge across systems.

Each producer must own a disjoint set of observed requests. Two collectors seeing
the same request would double-count its counters. Replayed snapshots are handled,
but overlapping requests across producers are not automatically deduplicated.

Compatibility metadata is a declaration, not proof of matching secrets, sender
identity, or disjoint traffic. Use trusted producer inventories and authenticated
file transfer. See the [trial checklist](TWO_OPERATOR_TRIAL.md).

## Do I Need A Secret, Account, Or Running Service?

Not to compare compatible exports. The comparison runs locally, reads files, and
writes stdout/stderr. It does not contact a model provider or any hosted service.

The producers need agreed hashing keys when creating compatible state. The person
combining their exports does not need those keys. Operators must authorize linkage
and agree on key handling and rotation separately.

The default demo needs Go. Its first build may download dependencies. The live
demo also needs a running Docker engine and may download its pinned Collector
image. Neither example calls a model. Synthetic OTLP traffic goes only to the local
collectors.

## What If A Producer Or Token Field Is Missing?

Use `--expected` to name every producer in the comparison scope. Missing producers
and partial observation intervals fail by default. `--allow-partial` permits a
report of observed differences, explicitly marked partial. It does not relax key,
scope, accounting, or other compatibility checks.

An entirely missing window remains an error, not a zero-usage window. Never remove
a missing producer from the expected list merely to obtain a complete report.

Spaces around producer IDs are ignored. Quote the list if it contains spaces:
`--expected "team, partner"`. Empty entries and duplicate IDs remain errors.

Token coverage uses the request and missing-usage counters supplied by the producer;
it is not inferred when those counters are absent. The current Collector contract
marks usage incomplete if either aggregate input or output is unavailable. A
reported zero is different from a missing field. Full collection intervals do not
prove upstream instrumentation, sampling, or delivery was complete.

## What Agent And MCP Activity Can I Compare?

The research demo compares observed root-agent runs, model requests, distinct MCP
sessions and resources, and tool-error signatures across two operators. Enable
`mcp.enabled: true` and `mcp.tool_errors.enabled: true` in the collector, and keep
`topk` above zero for tool-error sketches. The instrumentation must supply the
MCP attributes and both `gen_ai.tool.name` and `error.type` for tool errors.
A missing error attribute is not evidence that a tool succeeded.

The released collector counts `agent_runs` only for `invoke_agent` spans with an
empty parent span ID. Nested specialists and cross-operator invocations do not
add runs when trace context is propagated. An agent under an HTTP request or an
`invoke_workflow` parent counts zero by this rule. Losing parent context can
inflate the count; fleetdiff cannot detect that from summaries alone.

The example uses two batch jobs with root supervisor spans, nested internal
specialists, and a company-side client span parenting each partner-side specialist
span. Span ownership is disjoint even though trace IDs are shared. Propagation
preserves the intended count in this fixture; it is not deduplication or a general
definition of a business task. The
[agent conventions](https://github.com/open-telemetry/semantic-conventions-genai/blob/main/docs/gen-ai/gen-ai-agent-spans.md)
remain in Development, and this fixture is not a captured framework integration.

Resource URIs, session IDs, tool names, and error types are not printed in reports.
MCP resources use the retrieval-document hash domain, so an identical resource
merges once with compatible keys and settings. Tool-error signatures hash the
canonical tool name, a zero separator, and the canonical error type. Each observed
error contributes weight one. The live test uses its own fixture key to check
which signature moved; normal comparisons need no key and do not recover names.

Model requests rise from 6 to 10 while root runs stay at 2. Distinct resources
rise from approximately 1 to 4 and sessions from 4 to 8. This does not prove a
retry loop, over-delegation, answer quality, or an MCP security issue.

## What Do The Error Bounds Mean?

For each retained frequent item, the change interval is:

```text
[after.lower - before.upper, after.upper - before.lower]
```

An interval entirely above zero is an increase; entirely below zero is a decrease.
Exactly `[0,0]` means unchanged. Otherwise the direction is uncertain. These are
deterministic bounds on observed configured weight, not evidence of causation.

Candidates are the union of the two frequent-item queries. A key absent from a
retained set may still have weight. The report includes a bound for keys outside
the candidate union; it does not discover every possible heavy mover. Rows are
ordered by their largest absolute interval endpoint, not a guaranteed true ranking.
The default display limit is 20; use `--top N` for 1 to 100 rows.

Distinct counts use HLL and report nominal relative standard error based on the
profile's normal precision: `1.04 / sqrt(2^p)`. This is the dense-regime scale,
also shown for sparse sketches, not a measured error for the particular input,
deterministic bound, confidence interval, or statistical significance test.

## Why Does The Report Say Configured-Weight?

Summary v1 does not declare a typed unit for frequent-item weights. A producer can
weight observations by tokens, requests, or another supported nonnegative measure.
fleetdiff does not infer that unit from a measurement's name. Interpret the results
using the producer's documented accounting settings.

The samples weight prompt signatures by reported tokens and tool-error signatures
by occurrences. Their small candidate sets happen to give exact intervals; tests
also cover nonzero sketch error and disappearing keys.

## Are Exports And Reports Safe To Share?

They still require access controls. Summary metadata is cleartext; hashes are
pseudonymous and linkable under the same key. Numeric activity can also be sensitive.
This is neither anonymization nor differential privacy.

Default reports omit paths, producer/epoch/scope/key metadata, arbitrary measurement
names, and hashes. Known measurement names are allowlisted. `--show-hashes` opts into
pseudonymous item hashes; it does not reveal prompt text. Item aliases are local to
one report. Producer aliases follow the alphabetically sorted `--expected` list.

The demo puts reports in a fresh private directory and never overwrites an earlier
run. Live runs also retain collector diagnostics and intentionally altered test
inputs. Share only approved reports or the documented `handoff` directory, not the
whole live output directory.

## How Do I Choose Windows Or Save A Report?

One input can be a summary file or a nonrecursive directory of summary files. If a
directory contains several windows, select exactly one for each side:

```sh
bin/fleetdiff compare --before ./exports --after ./exports \
  --before-window 2026-09-13T12:00:00Z \
  --after-window 2026-09-13T12:01:00Z \
  --expected platform,data --format json
```

Before must end at or before after begins; both durations and measurement settings
must match. Historical windows are accepted. For completed collector exports, the
windows reflect processing time, not reconstructed event time.

To save a report privately, choose a new filename and use shell redirection:

```sh
umask 077
set -C
bin/fleetdiff compare --before ./before --after ./after \
  --expected team,partner --format json > comparison.json
```

`set -C` prevents the shell from overwriting an existing report. Validation errors
produce no report, although shell redirection may create an empty file first.

## What Are The File Limits?

Only local regular files are accepted. Symlinks and special files are rejected.
Each side allows at most 1,024 directory entries, 512 JSON files, and 32 MiB of
encoded input. Each file is limited to 8 MiB. Every JSON file is validated, including
files outside an explicitly selected window. Validation completes before report
output begins; diagnostics do not echo paths or input content.

Comparison errors name the affected window and a reviewed cause, such as
`unexpected summary producer`, `conflicting summary sequence`, or
`incompatible summary measurement contract`. Check the expected producer list,
snapshot sequences, or shared scope/key/accounting settings respectively. Unknown
dependency errors use a generic message rather than risk exposing input values.

These limits bound encoded input, not a measured maximum memory usage or constant
processing time. See the [comparison contract](COMPARISON.md) for details.

## What If The Demo Fails?

- **Go is missing:** install Go 1.25 or 1.26 with a current security patch, open a
  new terminal, and rerun `sh examples/demo.sh`.
- **The first build cannot download dependencies:** check network access to the
  configured Go module proxy. No private sibling checkout is required.
- **Docker is unavailable:** start Docker, or omit `--live` to use the sample files.
- **The live run missed a window:** keep the machine awake and rerun. A new private
  output directory is chosen each time; previous runs remain untouched.
- **A forced kill left containers behind:** inspect containers and networks named
  `fleetdiff-demo-*`. Ordinary exits and interruptions clean up the live collectors.

The default sample creates no background processes. Once the command returns,
there is nothing to stop. Delete a demo's printed output directory when you no
longer need its reports. Go's build cache and the pinned Docker image remain reusable.
