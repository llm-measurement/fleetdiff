# fleetdiff

[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/llm-measurement/fleetdiff/badge)](https://scorecard.dev/viewer/?uri=github.com/llm-measurement/fleetdiff)

fleetdiff is a local command-line tool that investigates LiteLLM spend exports,
compares LLM usage summaries, flags unusual windows, and checks trace captures
and collector configurations.

Use it to answer **why LLM token usage spiked, and which users or sessions drove
the change.** File-based commands stay offline. The unreleased native LiteLLM
path reads your database explicitly, then reports locally: no fleetdiff account,
upload, collector or model API key is needed.

| | |
| --- | --- |
| Commands | `investigate`, `what-if`, `scan`, `compare`, `inspect`, `diagnose`; candidate: `export litellm` |
| Platforms | Linux and macOS, AMD64 and ARM64 |
| Status | Pre-1.0; see the [changelog](CHANGELOG.md) |
| License | Apache-2.0 |

## Using LiteLLM?

**One command to the first report, after setup.** Compare two periods, see which
models and keys drove the change, and check the usage records you already retain.
The native path below is an **unreleased v0.12 candidate**, not a feature of the
released v0.11.0 installer.
For a released binary, use the fallback below.

### Native Candidate Setup (Development Only)

From a reviewed candidate source checkout with a patched Go 1.26 toolchain,
build into a new directory; stop if that directory already exists:

```sh
mkdir ./fleetdiff-install &&
go build -o ./fleetdiff-install/fleetdiff ./cmd/fleetdiff
```

Have your administrator provision the [read-only role and credential file](docs/NATIVE_EXPORT.md#credentials).
Set `DATABASE_URL_FILE` to that existing, owned mode-0600 regular file. Leave
`FLEETDIFF_LITELLM_DATABASE_URL` unset when using the file, then run:

```sh
: "${DATABASE_URL_FILE:?Set the path to your approved database URL file}"
./fleetdiff-install/fleetdiff investigate --litellm-db \
  --database-url-file "$DATABASE_URL_FILE" --group-by key
```

This connects only for the requested export, compares the last 14 complete UTC
days as two seven-day periods, and removes its private temporary export after
analysis. Add `--daily` for row coverage or `--routing-audit` for route evidence;
routing details are included automatically. Use `--keep-export NEWDIR` to retain
the private files, or `export litellm --out NEWDIR` to export without a report.
The [native guide](docs/NATIVE_EXPORT.md) covers credentials, periods and what-if.

<a id="1-export-request-level-rows"></a>

### Released Fallback: Export With psql

For v0.11.0, or client certificates, PostgreSQL services and other native-unsupported
connections, follow the [read-only SQL recipe](examples/litellm-spend/README.md#export-request-level-rows).
Keep the export private; it excludes prompts and responses but retains identities.
Existing request-level CSV, JSON and JSONL also work. Install the released binary
in a separate new directory from any candidate build, then investigate the file.

<a id="2-install-v060"></a>
<a id="2-install-v061"></a>
<a id="2-install-v070"></a>
<a id="2-install-v080"></a>
<a id="2-install-v090"></a>
<a id="2-install-v0100"></a>

### 2. Install v0.11.0

Run this in Bash with `curl`, `gh`, `shasum` and `tar` available:

```sh
bash -o pipefail -c 'curl --fail --location --proto "=https" --tlsv1.2 https://raw.githubusercontent.com/llm-measurement/fleetdiff/7773f212e4ae6e800f066b394e5507c0df8ada5f/scripts/install.sh | sh -s -- v0.11.0 ./fleetdiff-install'
./fleetdiff-install/fleetdiff --version
```

The v0.11.0 installer does not provide native database export. It checks signed
release provenance and checksums before extracting into a new directory. Follow
[Install and verify](docs/OPERATIONS.md#install-and-verify).

### 3. Investigate

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

## Try One Example

Download the synthetic spend file and use the installed binary:

```sh
curl --fail --location --proto '=https' --tlsv1.2 \
  https://raw.githubusercontent.com/llm-measurement/fleetdiff/v0.11.0/examples/litellm-spend/synthetic.csv \
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

The verified v0.11.0 installer selects the platform binary for your machine.
For manual downloads, use the [v0.11.0 archives](https://github.com/llm-measurement/fleetdiff/releases/tag/v0.11.0)
and [verification instructions](docs/OPERATIONS.md#install-and-verify).
An administrator can verify and transfer artifacts through an approved internal mirror.

With a security-patched Go 1.26 toolchain, install the same version from its module:

```sh
go install github.com/llm-measurement/fleetdiff/cmd/fleetdiff@v0.11.0
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

- [Native LiteLLM export](docs/NATIVE_EXPORT.md): unreleased v0.12 candidate setup and connection contract.
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
- <a id="docs"></a> [FAQ](docs/FAQ.md), [security](SECURITY.md), [release notes](docs/releases/v0.11.0.md) and [release procedure](docs/RELEASING.md).

Questions or feedback: [open an issue](https://github.com/llm-measurement/fleetdiff/issues).
See [Contributing](CONTRIBUTING.md) for checks and signed, signed-off commits.
Code authors: Vijay and Codex.
