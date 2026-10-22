# fleetdiff

[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/llm-measurement/fleetdiff/badge)](https://scorecard.dev/viewer/?uri=github.com/llm-measurement/fleetdiff)

fleetdiff reads your LiteLLM database or export to show what changed in spend, who drove it, which models served each route, and where spend-log rows are missing.

- [Your LiteLLM database](#using-litellm)
- [An existing export](#an-existing-export)
- [No LiteLLM? Try the sample](#try-one-example)

## Using LiteLLM?

<a id="2-install-v060"></a>
<a id="2-install-v061"></a>
<a id="2-install-v070"></a>
<a id="2-install-v080"></a>
<a id="2-install-v090"></a>
<a id="2-install-v0100"></a>
<a id="2-install-v0110"></a>
<a id="1-install-v0120"></a>

### 1. Install v0.12.1

Linux or macOS; needs curl, gh, shasum and tar. The installer checks the release before unpacking it.

```sh
bash -o pipefail -c 'curl --fail --location --proto "=https" --tlsv1.2 https://raw.githubusercontent.com/llm-measurement/fleetdiff/0d7db95c5bb3013f8c564c91fbb07b6235a2664e/scripts/install.sh | sh -s -- v0.12.1 ./fleetdiff-install'
./fleetdiff-install/fleetdiff --version
```

### 2. Investigate your database

Save your read-only connection URL in a file only you can read (see the [database setup](docs/NATIVE_EXPORT.md#credentials)), then run:

```sh
chmod 600 ~/.config/litellm-db-url
./fleetdiff-install/fleetdiff investigate --litellm-db --database-url-file ~/.config/litellm-db-url --group-by key
```

The report compares the last two complete seven-day UTC periods. Add `--daily`
for row coverage or `--routing-audit` for route evidence. The
[database guide](docs/NATIVE_EXPORT.md) covers setup, periods and saved exports.

## An Existing Export

<a id="3-investigate"></a>

Install fleetdiff using [step 1](#1-install-v0120), then investigate your
request-level CSV, JSON or JSONL file offline:

```sh
export_dir="$HOME/litellm-export"
./fleetdiff-install/fleetdiff investigate \
  --litellm-spend "$export_dir/spend.csv" --group-by key
```

With enough history, fleetdiff compares the two most recent complete seven-day
UTC periods inside the file and prints their exact boundaries and weekdays.
For shorter exports or chosen periods, set `--before-period` and `--after-period`
to dates or explicit UTC start/end intervals.

The report shows request count versus tokens per request by model, the leading
contributors, and usage completeness. Choose `--group-by team`, `user`, `end-user`
or `session` for another view; add `--format json` for automation.
[Export guide](examples/litellm-spend/README.md); [compare recorded cache reuse](docs/CACHE.md).
For a single-period [session-budget review](docs/SESSION_BUDGET.md), add
`--session-budget-usd 1.00` instead of a two-period grouping.
To check for missing spend-log rows, [export the daily counts too](docs/ROW_COVERAGE.md)
and add `--litellm-daily-spend "$export_dir/daily.csv"`; totals stay as recorded.

<a id="1-export-request-level-rows"></a>

### Manual Alternative: Export With psql

For client certificates, PostgreSQL services and other native-unsupported
connections, follow the [read-only SQL recipe](examples/litellm-spend/README.md#export-request-level-rows).
Keep the export private; it excludes prompts and responses but retains identities.

## Try One Example

Install fleetdiff using [step 1](#1-install-v0120), then run the synthetic example:

```sh
curl --fail --location --proto '=https' --tlsv1.2 \
  https://raw.githubusercontent.com/llm-measurement/fleetdiff/v0.12.1/examples/litellm-spend/synthetic.csv \
  --output synthetic.csv
./fleetdiff-install/fleetdiff investigate --litellm-spend synthetic.csv \
  --before-period 2026-10-07 --after-period 2026-10-08
```

Selected output:

```text
Recorded tokens doubled (8,000 -> 16,000).
model-1 accounts for 100% of the net recorded increase (+8,000 tokens).
  model-1: 4,000 -> 12,000 recorded tokens; 4 -> 6 requests.
    1,000.00 -> 2,000.00 recorded tokens per request; +3,000 from request count, +5,000 from request size.
  2 leading tracked keys account for 100% of the net recorded increase.
  Zero-only records, origin unknown: 2 -> 4.
  Failed records (overlap usage categories): 1 -> 2.
```

Two keys explain the increase. The zero-only and failed rows point to records
worth reviewing in LiteLLM. The [fixture and checks](examples/litellm-spend/TESTING.md)
make these answers reproducible.

The summary-file path also shows session concentration:

![Synthetic session investigation: one of eight tracked sessions flagged for review, accounting for 90.91% of attributed tokens.](docs/media/sessions.gif)

[Run the sessions demo](examples/sessions/README.md) |
[Read its transcript](docs/media/sessions-transcript.txt).

<a id="install-a-binary"></a>

## Install

The verified v0.12.1 installer selects the platform binary for your machine.
For public release downloads, `gh` need not be signed in.
For manual downloads, use the [v0.12.1 archives](https://github.com/llm-measurement/fleetdiff/releases/tag/v0.12.1)
and [verification instructions](docs/OPERATIONS.md#install-and-verify).
An administrator can verify and transfer artifacts through an approved internal mirror.

With a security-patched Go 1.26 toolchain, install the same version from its module:

```sh
go install github.com/llm-measurement/fleetdiff/cmd/fleetdiff@v0.12.1
```

The executable goes to `GOBIN`, or `$(go env GOPATH)/bin` when unset.
See [source builds and version reporting](docs/OPERATIONS.md#build-from-source).

## Limits

- Spend reports describe the supplied logged model requests. Missing usage,
  ambiguous zero-only counts and failures remain visible; unsupported call types
  lead with an incomplete comparison. Provider records remain the source for billing.
- Use request-level exports. The LiteLLM Usage dashboard's daily aggregates
  receive instructions for getting those rows. The reader accepts up to 10M rows
  and 16 GiB per file, with [per-record limits](examples/litellm-spend/README.md#data-and-limits).
- By default, private names use aliases or pseudonymous hashes; public catalog model IDs are readable.
  Keep source files private and share reports under your organization's policy.
- Rankings carry deterministic bounds; distinct counts are statistical estimates.
  A flagged contributor identifies something to investigate, rather than its cause.
  Compatible summary files need aligned windows, keys and disjoint producer coverage.

## Documentation

fleetdiff also compares LLM usage summaries, flags unusual windows, and checks
trace captures and collector configurations. File-based commands stay offline;
the database path reads from your LiteLLM instance and reports locally.

Guides: [LiteLLM spend](https://llm-measurement.github.io/litellm-spend.html) | [Routing](https://llm-measurement.github.io/routing.html) | [Row coverage](https://llm-measurement.github.io/row-coverage.html).

| | |
| --- | --- |
| Commands | `investigate`, `what-if`, `scan`, `compare`, `inspect`, `diagnose`, `export litellm` |
| Platforms | Linux and macOS, AMD64 and ARM64 |
| Status | Pre-1.0; see the [changelog](CHANGELOG.md) |
| License | Apache-2.0 |

- [Native LiteLLM export](docs/NATIVE_EXPORT.md): setup, credentials and connection contract.
- [LiteLLM spend exports](examples/litellm-spend/README.md): SQL, periods, groupings and source accounting.
- [Session-budget review](docs/SESSION_BUDGET.md): single-period recorded totals and cap review.
- [Model-mix what-if](docs/WHAT_IF.md) (v0.10.0): re-price covered usage under explicit assumptions.
- <a id="try-it-in-a-minute"></a><a id="did-one-session-account-for-most-of-the-increase"></a> [Single-application and session examples](docs/WORKFLOWS.md#try-it-in-a-minute).
- <a id="will-it-work-with-my-traces"></a> [Inspect your traces](docs/INSPECT.md) and [capture recipes](examples/inspect/README.md).
- <a id="set-up-and-keep-watching"></a> [Diagnose configurations](docs/DIAGNOSE.md), [scan history](docs/SCAN.md) and [compare cache use](docs/CACHE.md).
- <a id="keep-watching-litellm"></a> [Continuous LiteLLM collection](docs/WORKFLOWS.md#keep-watching-litellm).
- <a id="extend-to-separately-operated-agents"></a><a id="run-it-through-real-collectors"></a> [Several operators and live collectors](docs/WORKFLOWS.md#extend-to-separately-operated-agents).
- <a id="questions-it-answers"></a><a id="user-and-session-contributors"></a> [Questions and contributor rankings](docs/WORKFLOWS.md#questions-it-answers).
- <a id="how-it-works"></a><a id="use-your-own-data"></a><a id="reading-the-results"></a> [How summary comparison works](docs/WORKFLOWS.md#how-it-works), [comparison contract](docs/COMPARISON.md) and [investigation contract](docs/INVESTIGATION.md).
- <a id="built-to-be-checked"></a> [Operations](docs/OPERATIONS.md), [resource measurements](docs/BENCHMARKS.md) and [checks](docs/WORKFLOWS.md#built-to-be-checked).
- <a id="docs"></a> [FAQ](docs/FAQ.md), [security](SECURITY.md), [release notes](docs/releases/v0.12.1.md) and [release procedure](docs/RELEASING.md).

Questions or feedback: [open an issue](https://github.com/llm-measurement/fleetdiff/issues).
See [Contributing](CONTRIBUTING.md) for checks and signed, signed-off commits.
Code authors: Vijay and Codex.
