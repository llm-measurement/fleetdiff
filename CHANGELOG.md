# Changelog

Notable user-visible changes are recorded here, newest first.

## [0.4.0] - 2026-10-04

See the [release notes](docs/releases/v0.4.0.md) for capture and upgrade instructions.

- Add offline `inspect` for local OTLP JSON/JSONL, raw or framed protobuf, capture
  directories, and stdin. Report field readiness, token coverage and origin,
  distinct-value label risks, and user/session/prompt rankings with bounds.
- Use fresh per-run hashing keys, default aliases, bounded inputs, and sanitized
  errors. Reuse the collector's versioned accounting fixtures in both CI suites.
- Add runnable Collector, Python SDK, and LiteLLM capture recipes and a Go-only
  inspection demo. Verify inspection in packaged-binary and no-network smoke tests.
- Leave existing summary formats and comparison/investigation JSON unchanged.
- Add opt-in custom attribute names with terminal-safe text, actionable aliased
  label guidance, readable question names, and compact exact ranking counts.
  Keep usage measurements out of label review while preserving accounting checks.
- Put the actual sessions demo at the top of the README as a small terminal GIF,
  with a plain-text transcript and a CI check against live demo output.

## [0.3.1] - 2026-10-01

See the [release notes](docs/releases/v0.3.1.md) for upgrade details.

- Lead investigation reports with the main finding, followed by compact
  contributor tables and coverage. Show session candidates as "flagged for review."
- Round displayed token-change contributions to whole tokens, retaining JSON
  precision, percentage bounds, and token-per-attempt averages.
- Include the one-command sessions demo, clearer report guides, and refreshed
  walkthrough media. Check the text headline and rounded contributions in each
  packaged binary's smoke test.
- Keep summary compatibility, JSON version 1, accounting, and flag thresholds
  unchanged. No collector upgrade or window reset is needed.

## [0.3.0] - 2026-10-01

See the [release notes](docs/releases/v0.3.0.md) for compatibility and upgrade details.

- Add tracked user/session contributor support for optional `top_users` and
  `top_sessions` sketches and their `_requests` variants. Session review flags
  require an after-window share lower bound strictly above `--flag-share`
  (default `0.25`) and complete relevant observations; they are not causal or
  runaway-session diagnoses. Preserve unknown attribution when optional sketches
  are absent from any input snapshot.
- Clarify summary-only inputs, expected producer IDs, released versus checkout
  behavior, Go installation and embedded checkout versions, and feedback routing.
- Detect the host OS and architecture during verified binary installation;
  document partial-asset checksum checks and macOS browser-download approval.
- Show expected producer counts in the single-app/two-stack demo.
- Name missing required flags in CLI errors and point text coverage guidance
  to the text report rather than JSON-only field paths.
- Replace historical resource numbers with a checked-in benchmark command and
  measurements of the public v0.2.0 tag.
- Check optional user/session attribution, unknown older windows, threshold
  boundaries, and hidden hashes in each native release archive.

## [0.2.0] - 2026-09-26

- Add a read-only `investigate` command with token-change questions, tracked
  contributor shares, and explicit unknown answers for incomplete evidence or
  unsupported session attribution. Add a single-app demo and two-stack extension
  using collector-generated synthetic LiteLLM-shaped fixtures.
- Report optional, versioned usage provenance; compare older exports as unknown
  provenance without changing their files or relaxing accounting checks.
- Exercise `investigate` and missing-usage handling in every native release-binary
  smoke test. Existing comparison JSON remains version 1.

## [0.1.1] - 2026-09-25

Local, read-only comparison of agent-fleet measurements. Supports Linux and
macOS on AMD64 and ARM64. JSON reports use comparison contract v1.

### Added

- Build identity through `--version`, four-platform archive packaging, native
  binary smoke tests, an SPDX dependency inventory, and public-release provenance
  signing.
- Offline and unprivileged operation, atomic report-writing example, JSON
  compatibility rules, upgrade/rollback guidance, and input-resource measurements.
- Scheduled vulnerability and extended malformed-input checks.
- A shared research-agent scenario for the saved-file and live collector demos,
  with five investigation questions, cross-operator trace context, MCP resources,
  and tool-error signature checks. Nested specialists do not add root-agent runs.
- Local, read-only comparison of two windows across separately operated systems,
  using compatible sketchkit summary exports.
- Observed request and token deltas, missing-usage coverage, distinct estimates,
  and tracked prompt-weight changes with lower and upper bounds.
- Text and JSON reports, explicit partial-data opt-in, and opt-in item hashes.
- A one-command sample walkthrough and a live two-collector example.
- A problem-first README and a usage chart generated from the demo's JSON reports.
- A security policy and weekly dependency-update checks for Go and GitHub Actions.

### Fixed

- Tagged `go install` builds report Go's embedded module version instead of `dev`.
  Explicit release stamps still take precedence; unstamped builds fall back to
  `dev` when Go supplies no usable module version.
- Use llm-sketchkit v0.2.1 with consistent Go/Python validation of frequent-items
  total weight; retain fleetdiff's pre-combination checks.
- Reject frequent-items payloads whose totals contradict retained item bounds,
  including undisplayed measurements and superseded snapshots.
- Comparison errors retain reviewed causes without exposing input values or paths.
- Spaces around comma-separated expected producer IDs are ignored; empty and
  duplicate entries remain errors.
- Fuzz tests use consecutive windows and require valid seeds to reach reports.
- Untracked-key bounds are checked against ground truth outside the full candidate
  set, including keys seen in only one window.
- The nominal HLL error scale is documented and tested for every supported profile
  in empty, sparse, and dense form. Reported numbers are unchanged.
