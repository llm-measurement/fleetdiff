# Compare Two Operators' Collector Exports

Question: **did our research-agent change reduce work, or just move it elsewhere?**

A company-operated supervisor delegates to an internal-document specialist and a
partner-operated external-research specialist. After a deployment, it delegates
more calls to the partner. Specialists use MCP to reach documents and tools.

This example runs two instances of the released `otelcol-genai-sketches 0.1.0`
image. Each receives its own synthetic OTLP/HTTP fixture. They have separate state
and private export directories. Only their summary files enter fleetdiff.

It exercises the real receiver, connector, file exporter, and fleetdiff comparison.
It is not a captured SDK integration, a provider compatibility certification, or
evidence that two external organizations have tried the software.

## Run Locally

Requirements: Linux or macOS, a running Docker engine, and Go 1.25 or 1.26 with a
current security patch. Docker downloads the pinned release; Go may download the
published sketchkit dependency. No sibling checkout or cloud account is required.
See the collector's [image verification instructions](https://github.com/llm-measurement/otelcol-genai-sketches/blob/v0.1.0/docs/DEPLOYMENT.md#verify-an-image).
The immutable image reference is in [image.txt](image.txt).

From the fleetdiff repository, the simplest path is:

```sh
sh examples/demo.sh --live
```

The script checks prerequisites, pulls the pinned image if needed, builds fleetdiff,
runs both collectors, and prints a short comparison. Every run gets a new private
directory under `.cache`; the command prints the report paths. The collectors are
removed automatically. To try the saved-file example without Docker, omit `--live`.

## Manual Run (Optional)

The commands below use a fixed directory name to make the later examples easy to
follow. This is an alternative to the one-command path, not an additional setup step:

```sh
docker pull "$(cat examples/two-operators/image.txt)"
go run ./examples/two-operators -out two-operator-run
go build -o bin/fleetdiff ./cmd/fleetdiff
bin/fleetdiff compare \
  --before two-operator-run/handoff/before \
  --after two-operator-run/handoff/after \
  --expected owned,partner
```

Allow about a minute after installation. The example waits for two complete
15-second processing-time windows. It never edits exported timestamps or invents
complete coverage. Span timestamps in the synthetic fixtures are deliberately
fixed; the collector's windows follow receipt time. A suspended machine or missed
send window can fail a run. Use a new output directory when rerunning.

The example refuses existing output directories, sends each batch once, and fails
on OTLP errors or partial acceptance. It removes its containers and Docker network
on exit, including ordinary interruption. A forced process kill may require manual
cleanup of containers/networks named `fleetdiff-demo-*`. The pinned image and output
directory remain for reuse and inspection.

## Expected Answer

Looking only at the owned system, reported tokens fall from 800 to 360. The
partner's tokens rise from 400 to 1,440. Bringing the partner's export into the
comparison changes the answer: the combined scope rises by 600 tokens. This is
why another operator's participation is useful, even when each system already has
its own dashboard. It is not evidence of a population-wide network effect.

| Measurement | Before | After |
|---|---:|---:|
| Submitted spans | 25 | 49 |
| Observed root-agent runs | 2 | 2 |
| Model requests | 6 | 10 |
| Requests with complete input/output usage | 4 | 8 |
| Requests missing usage | 2 | 2 |
| Reported input tokens | 1,000 | 1,500 |
| Reported output tokens | 200 | 300 |
| Reported input plus output | 1,200 | 1,800 |
| Cache-read tokens, already within input | 40 | 40 |
| Reasoning tokens, already within output | 20 | 20 |
| Combined distinct users, approximately | 2 | 2 |
| Distinct MCP resources, approximately | 1 | 4 |
| Distinct MCP sessions, approximately | 4 | 8 |
| Planted search-timeout signature occurrences | 1 | 4 |
| Planted lookup-error signature occurrences | 2 | 2 |
| Planted page-fetch-error signature occurrences | 0 | 1 |

Agent, tool, and MCP spans do not inflate model-request counts. Shared users merge
across operators, as does resource R1, which both specialists read. Adding their
individual distinct estimates would be wrong. The fixture's three prompt-weight
changes are -500, +60, and +1,040 tokens. Prompt and tool-error bounds are exact in
this small example; the wider test suite covers nonzero sketch error. Reports use
aliases, not the planted tool names or errors. The harness alone checks identities.

The walkthrough asks five questions: did reported usage fall, did model activity
rise with flat runs, did more resources appear, did error signatures increase, and
is a system missing? The answer is **reported work moved and grew**. Two requests
still lack usage in each window, though their share falls from 2/6 to 2/10. These
measurements do not establish total spending, wasted work, answer quality,
complete instrumentation, or invoice accuracy. Cache and reasoning subsets are
not counted twice.

The [shared recipe](../internal/scenario/research.json) drives both demo paths.
A Docker-free test independently derives its answers from the generated spans;
this live run checks the collector exports against those answers.

### Agent Roots And Span Ownership

Each batch job starts a trace with a root `invoke_agent` supervisor span. Local
specialists are nested. An owned client invocation and its partner-side child
share trace context, but have different span IDs. Each span goes to exactly one
collector. The model requests are also disjoint, so no event is counted twice.

This is one synthetic representation, not tested framework compatibility or a
standard agent task denominator. The released collector counts `agent_runs` only
for parentless `invoke_agent` spans. Agents under an HTTP request or workflow
parent count zero; lost trace context can inflate the count. Shared trace IDs do
not authenticate operators or deduplicate independently recorded work. See the
[FAQ](../../docs/FAQ.md#what-agent-and-mcp-activity-can-i-compare).

`comparison.json` and `comparison.txt` contain the actual report. `checks.json`
records the image, selected windows, and checks that passed. Times, process epochs,
and keyed hashes vary between runs; the accounting assertions do not.

`owned-only.json` shows the narrower comparison. To reproduce it:

```sh
bin/fleetdiff compare \
  --before two-operator-run/handoff/before/owned.json \
  --after two-operator-run/handoff/after/owned.json \
  --expected owned
```

That command asks about the owned system only. For the combined scope, keep both
producers in the expected inventory even when one fails to deliver an export.

## Try A Missing Operator

The example creates a copy with the partner's after-window export removed:

```sh
bin/fleetdiff compare \
  --before two-operator-run/handoff/before \
  --after two-operator-run/counterexamples/missing \
  --expected owned,partner
```

This must fail with no report. Add `--allow-partial` to see the observed subset;
the missing operator is explicit and the result is marked partial. The example
saves that result as `missing-operator.json`. Lower observed totals here must not
be described as savings.

Other counterexamples change a key ID, scope, accounting declaration, or observation
interval. Key, scope, and accounting mismatches still fail with `--allow-partial`.
An incomplete interval requires that flag. Replaying a snapshot leaves the complete
comparison byte-identical. These edited copies are test inputs only: never change
production metadata to force a merge.

## What Is Shared

The `handoff` directory contains only four original collector summary files,
unchanged. No prompts, user IDs, document IDs, MCP resource URIs, or hashing secret
are copied into that handoff. The inputs are synthetic, but the privacy check also
scans decoded sketch bytes rather than just searching base64-encoded JSON.

Summary metadata is cleartext; hashes are pseudonymous and linkable. Aggregates
can still reveal sensitive activity. Use an approved, authenticated transfer and
access controls when applying the pattern to real data. A valid file does not prove
the sender's identity, that the hashing secrets really match, or that event streams
are disjoint. Do not transfer the entire output directory: it includes private
diagnostics and intentionally altered counterexamples.

Each collector gets a fresh per-run key and key-version ID through its environment.
Exports from different demo runs therefore reject comparison. The same key is
used for this one shared scope, never across unrelated customers. It is not saved
in the handoff or reports. Operators must separately agree on key handling and
rotation in a real deployment. fleetdiff does not need the key to compare exports.

Containers have read-only root filesystems, no Linux capabilities, private writable
summary mounts, and CPU/memory limits. Ports are bound to host loopback. The Docker
network is a normal bridge, not an outbound firewall; no external exporter is
configured. This is a local fixture runner, not a production security configuration.

Metric slice values are allowed cleartext values. The fixtures use non-sensitive
model labels and force label overflow. Raw-field sentinels belong in hashed
attributes, not in those labels. The test requires a top-k log snapshot within the
scan and checks that raw sentinels and tracked item hashes do not enter metrics.

## Repeat In CI

```sh
go test -race -tags integration ./examples/two-operators -count=1 -timeout 4m
```

The Linux CI job pulls the same pinned image and runs this test. Ordinary unit
tests need no Docker. The additional HTTP transport tests cover partial acceptance,
redirect rejection, and no application-level retry of writes.

For a human-to-human trial, use the [two-operator trial checklist](../../docs/TWO_OPERATOR_TRIAL.md).
