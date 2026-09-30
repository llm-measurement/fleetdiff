# fleetdiff

**See what changed in your agent application, using summaries instead of raw traces.**

Did token usage rise because you made more requests, or because each request
used more tokens? Which tracked contributors changed? Is the comparison missing
usage data? fleetdiff reads small summary files and answers locally. Start with
one application; combine compatible exports when you add workers or separately
operated systems. Keep your existing trace backend. No account, upload, or model
API key is needed.

Inputs are summary exports from
[otelcol-genai-sketches](https://github.com/llm-measurement/otelcol-genai-sketches)
or [llm-sketchkit](https://github.com/llm-measurement/llm-sketchkit), not raw traces,
vendor dashboards, or bills.

## Try It In A Minute

```sh
git clone https://github.com/llm-measurement/fleetdiff.git
cd fleetdiff
sh examples/investigate.sh
```

Needs Git and [Go](https://go.dev/dl/) 1.25 or 1.26 with a current security patch,
on Linux or macOS. No Docker. The command builds current source and prints a
single-application report from synthetic collector exports:

```text
Reported tokens: 200 -> 600
Model attempts: 2 -> 3
Tokens per attempt: 100.00 -> 200.00
Attempt-count contribution: +150.00 tokens
Tokens-per-attempt contribution: +250.00 tokens
```

This is an arithmetic split, not proof of cause. Try
`sh examples/investigate.sh --missing-usage`: one missing usage field makes the
explanation `cannot_determine`, not a guessed saving. Add `--two-stacks` to run
the same investigation over disjoint gateway and direct-call exports.
See [the example and its limits](examples/single-app/README.md).

Model attempts are observed model spans, including failures and retries, not
unique user requests. Existing JSON fields keep their `requests` names.

`investigate` is included in release v0.2.0. That binary prints "Requests" where
current source prints "Model attempts"; the arithmetic is unchanged. The optional
user/session contributor support described below is **unreleased**, not part of
v0.2.0.

## Install A Binary

No Go compiler is needed to use a release binary. Download the
[v0.2.0 archive](https://github.com/llm-measurement/fleetdiff/releases/tag/v0.2.0)
for Linux or macOS, on AMD64 or ARM64. Follow the
[download and verification instructions](docs/OPERATIONS.md#install-and-verify)
before extracting it. Then run `./fleetdiff --version` or investigate your exports:

```sh
./fleetdiff investigate --before ./before --after ./after --expected app
```

`app` is the summary's `producer_id`, not a filename or user/session ID. Use your
trusted producer inventory for `--expected`, including producers whose exports
are missing.

With Go installed, install the latest published version:

```sh
go install github.com/llm-measurement/fleetdiff/cmd/fleetdiff@latest
```

The binary goes to `GOBIN`, or `$(go env GOPATH)/bin` when `GOBIN` is unset; add
that directory to `PATH`. This does not install unreleased checkout changes.

## Using LiteLLM?

Did token usage jump after an application change? Send LiteLLM traces to the
[collector recipe](https://github.com/llm-measurement/otelcol-genai-sketches/tree/main/examples/integrations/litellm),
then use `fleetdiff investigate` to compare its before-and-after summary exports.
See whether the increase came from more requests or more recorded tokens per
request, with missing usage shown. Keep your existing tracing backend.

The recipe documents the tested LiteLLM version and an optional callback that
preserves missing provider usage for supported non-streaming responses. Streaming
usage provenance remains unknown: LiteLLM can supply estimates when provider
counts are absent. The comparison is not invoice reconciliation or proof of savings.

## Extend To Separately Operated Agents

Run `sh examples/demo.sh` for the existing two-operator walkthrough.

![Synthetic demo: your team's reported tokens fall from 800 to 360, while the partner's rise from 400 to 1,440. Fleet totals rise from 1,200 to 1,800. Two requests lack token usage in each window.](docs/media/fleet-usage.png)

[Two-operator walkthrough video and transcript](docs/media/README.md#terminal-walkthrough).

A research supervisor delegates to an internal-document specialist your team
runs and a research specialist a partner runs. This is the supervisor-and-specialists
pattern described by [Anthropic](https://www.anthropic.com/engineering/multi-agent-research-system)
and [LangChain](https://docs.langchain.com/oss/python/langchain/multi-agent/subagents).
After a deployment:

```text
1. Did total reported usage fall?
   Our reported tokens: 800 -> 360 (down 440)
   Fleet reported tokens: 1200 -> 1800 (up 600)

2. Did model activity rise while runs stayed flat?
   Model requests: 6 -> 10
   Observed root-agent runs: 2 -> 2
   Requests missing token usage: 2 -> 2
```

**Your system reported less. The fleet reported more.** Reported token usage
shifted toward the partner and increased overall. Whether the answers got better
is outside what fleetdiff measures. The data is synthetic, not provider traffic
or evidence of savings.

## Questions It Answers

| Question | In the demo |
|---|---|
| More requests or more tokens per request? | Single-app report: +150 and +250 tokens respectively; refused if usage is incomplete |
| Which tracked contributors changed? | Prompt-weight delta bounds and shares of recorded sketch weight, not a guaranteed top-k ranking |
| Can it identify a runaway session? | `cannot_determine`: current exports do not attribute model tokens to sessions |
| Did total reported usage fall, or just move? | Ours 800 to 360 tokens; fleet 1,200 to 1,800 |
| Did model activity rise while runs stayed flat? | 6 to 10 model requests; 2 root-agent runs in both windows |
| Did the workload touch more documents? | About 1 to 4 distinct MCP resources; a shared one counts once |
| Did particular tool-error signatures increase? | One rises from 1 to 4; one appears; one is unchanged |
| Is part of the fleet missing? | A missing export is refused, or reported as explicitly partial |

### Unreleased Contributor Support

Current-source support adds tracked user and session contributors when compatible
exports contain `top_users` or `top_sessions` token-weighted sketches, or their
`_requests` variants. Existing demos above have no user/session attribution
sketches. Missing user/session sketches remain unknown; distinct session counts
alone cannot attribute token usage.

A session is flagged for review only when its after-window share **lower bound
is strictly greater than 25%** by default. `--flag-share` sets the threshold as a
fraction, for example `--flag-share 0.40`. Shares use recorded sketch weight,
not necessarily all application usage. Incomplete observation intervals suppress
flags; token-weighted flags also require complete numeric token coverage. This is
a concentration flag, not proof of a runaway session or its cause. See the
[unreleased contract](docs/INVESTIGATION.md#unreleased-user-and-session-contributors).
Build the checkout to use this support; the linked v0.2.0 binary does not have it.

## How It Works

```text
 Operator A                    Operator B
 traces -> collector           traces -> collector
            |                              |
     summary file                   summary file      counters + keyed sketches,
            \                              /          no raw values
             '------> fleetdiff compare <-'           runs locally, read-only
                            |
                 fleet report (text or JSON)
```

Each operator keeps its own trace backend. The
[OpenTelemetry collector](https://github.com/llm-measurement/otelcol-genai-sketches)
or [llm-sketchkit](https://github.com/llm-measurement/llm-sketchkit) exports a
summary per time window. Replayed summaries do not double-count. Shared identities
count once with compatible hashing settings. Operators must avoid counting the
same requests twice: fleetdiff cannot deduplicate requests observed by different
operators. It will not call a comparison complete when an expected operator is
missing.

## Run It Through Real Collectors

With Docker running, `sh examples/demo.sh --live` starts two pinned, released
collectors, sends the same synthetic traffic, and compares their real exports.
It checks every answer above, plus propagated trace context and privacy scans
for raw fields. See the [two-operator walkthrough](examples/two-operators/README.md).

## Use Your Own Data

From the checkout, build the command if you skipped the demo:

```sh
go build -o bin/fleetdiff ./cmd/fleetdiff
```

1. Each operator enables [summary export](https://github.com/llm-measurement/otelcol-genai-sketches/blob/main/docs/SUMMARY_EXCHANGE.md)
   and agrees on time windows, hashing settings, and who observes which requests.
2. Put one window's files in `before/` and the later window's files in `after/`.
3. Investigate, naming every expected producer, even if one export is missing:

```sh
bin/fleetdiff investigate --before ./before --after ./after --expected app
```

Add `--format json` for automation. fleetdiff only reads files; it never modifies
inputs or makes network requests. Sharing an export still requires
authorization. For multiple disjoint producers, use `--expected team,partner`.
These are the exact `producer_id` values assigned by the exporter. Keep the same
expected set for both windows; do not drop an ID to hide a missing export.
The lower-level `compare` command provides the full evidence report. See the
[investigation contract](docs/INVESTIGATION.md) and, for separate operators, the
[two-operator trial checklist](docs/TWO_OPERATOR_TRIAL.md).

## What The Numbers Mean, And What They Don't

- **Exact:** request, token, and agent-run counts, for observed spans.
- **Estimated:** distinct users, sessions, and resources, with a nominal error.
- **Bounded:** changes in prompt and tool-error signatures, with lower and upper
  bounds.
- **Missing stays missing:** requests without token usage are counted as
  missing, never as zero.

Reported tokens are not an invoice or a measure of useful work, and a
before-and-after difference does not prove its cause. More activity does not
prove retries, over-delegation, or better answers. Agent runs count only
`invoke_agent` spans with no parent, so an agent under an HTTP request or
workflow span is not counted. See the
[agent and MCP caveats](docs/FAQ.md#what-agent-and-mcp-activity-can-i-compare).
Hashes are keyed and pseudonymous, not anonymous; treat exports and reports as
sensitive.

## Built To Be Checked

- Adversarial and fuzz tests on malformed inputs, with bounded file, byte, and
  directory-entry limits.
- Weekly dependency and security scans.
- Release binaries for Linux and macOS on AMD64 and ARM64, with SBOM and
  provenance, tested on native runners.
- [Resource measurements](docs/BENCHMARKS.md) from one machine, with the
  commands to reproduce them.

Run the checks yourself with `go test -race ./...` and `go vet ./...`.

## Docs

- [FAQ](docs/FAQ.md): inputs, accuracy, privacy, and troubleshooting
- [Comparison contract](docs/COMPARISON.md)
- [Investigation questions and JSON API](docs/INVESTIGATION.md)
- [Operations](docs/OPERATIONS.md): installation verification, offline use, and upgrades
- [Resource measurements](docs/BENCHMARKS.md): sizing on one machine
- [Security policy](SECURITY.md) · [Changelog](CHANGELOG.md) · [Releasing](docs/RELEASING.md)

The `0.2.x` release line provides local, read-only `investigate` and `compare` commands.
Questions or feedback: [open an issue](https://github.com/llm-measurement/fleetdiff/issues).
Do not include raw traces, secrets, or unapproved exports; see the
[security policy](SECURITY.md) for confidential vulnerability reports.

Apache-2.0. Code authors: Vijay and Codex.
