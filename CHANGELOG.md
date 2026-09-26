# Changelog

Notable user-visible changes are recorded here.

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
  Explicit release stamps still take precedence; checkout builds remain `dev`.
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
