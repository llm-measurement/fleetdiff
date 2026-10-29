# Changelog

Notable user-visible changes are recorded here, newest first.

## [Unreleased]

- Add explicit agent attribution from LiteLLM `agent_id` or prefixed request
  tags on LiteLLM and Langfuse, with exclusive known/unknown/ambiguous totals,
  repeated/empty-tag coverage and an exact 1,024-agent bound.
- Add opt-in request-tag export through the shared psql/native projection,
  and an agent table with exact recorded USD, coverage, period shares and
  `--rank-by recorded-spend-change`. Default exports and reports are unchanged.
- Require `--acknowledge-sensitive-labels` together with `--show-labels` for
  raw agent or routing labels. User/session/end-user/key rankings remain aliased.
  See the [agent attribution contract](docs/AGENT_ATTRIBUTION.md).

## [0.13.1] - 2026-10-27

See the [release notes](docs/releases/v0.13.1.md).

- Read only Langfuse's bounded `level` enum, show ERROR observation token
  subsets and avoid severity-only usage-review flags for DEFAULT/DEBUG/WARNING.
  Provider call status and billing finality remain unknown; missing/invalid
  levels still require review. Token/cost totals and LiteLLM behavior are unchanged.
- Extend JSON/JSONL/CSV and gzip privacy checks without reading `status_message`
  or observation payloads. This does not change the published v0.13.0 artifacts.
- Reuse the model section's final labels in contributor and cache rankings
  under Langfuse `provided_model_name` grouping. Preserve exact bundled public
  IDs, private aliases, keyed identities and project separation.

## [0.13.0] - 2026-10-24

See the [release notes](docs/releases/v0.13.0.md).

- Compare completed local Langfuse exports, their contributors and manifest
  coverage, with project-scoped deduplication and disjoint usage accounting.
- Add optional local provider request counts for LiteLLM, qualified by population,
  counting units and settlement at retrieval time.
- Include intervening v0.12.1 documentation/security notes and documentation-media
  dependency monitoring. No new Go module dependencies.

## [0.12.1] - 2026-10-22

See the [release notes](docs/releases/v0.12.1.md).

- Build with Go 1.26.10 for GO-2026-6809, covering TLS verification when system
  trust stores have not been updated to distrust compromised intermediates.
- Use Go 1.27.3 for the existing 1.27-line scheduled security checks.

## [0.12.0] - 2026-10-18

See the [release notes](docs/releases/v0.12.0.md) and
[native export guide](docs/NATIVE_EXPORT.md).

- Add explicit native `export litellm` for private CSV/JSONL snapshots, optional
  daily counters and routing details, with the pinned LiteLLM v1.104.0 projection.
- Add `--litellm-db` to `investigate` and `what-if`, with automatic private-export
  cleanup or `--keep-export NEWDIR`. Default to the last 14 complete UTC days;
  preserve what-if `--from` / `--to` as model selectors and `--period` as time.
- Introduce an explicit runtime network boundary for database modes only; file
  commands remain offline. Require dedicated credential sources, verified remote
  TLS, bounded read-only snapshots, sanitized errors and new private output paths.
- Stage exports in a private mode-0700 sibling `.fleetdiff-stage-...` directory, then
  atomically publish the complete directory without replacement. Keep the final
  destination absent until publication; crashes beforehand may leave sibling
  staging data, never a partial final directory.
- Order SQL exports deterministically by `"startTime", request_id
  COLLATE "C"` and daily `date COLLATE "C"` for native/psql byte equivalence.
  Compared with the earlier unsorted recipe, bytes and first-seen/tied report
  ordering may change; recorded totals and identity hashing are unchanged.
- Pin `pgx/v5 v5.11.0`, `pgpassfile v1.0.0` and
  `pgservicefile v0.0.0-20240606120523-5a60cdf6a761`; retain `x/text v0.41.0`.

## [0.11.0] - 2026-10-17

See the [release notes](docs/releases/v0.11.0.md) for routing evidence and
provider-qualified model pricing.

- Compare the models serving each original LiteLLM route, with optional agent
  grouping, attribution coverage and separate classifier overhead.
- Select a route or agent for investigation, what-if moves and workload maps.
  Keep the routing export and audit opt-in.
- Display exact public catalog model IDs; keep private names aliased. Add
  `--show-labels` for explicitly requested local route and agent labels.
- Correct what-if exclusion of provider-qualified IDs such as
  `openai/gpt-6.1-sol` when the row's provider and catalog provider both match.
  Labels and pricing share this exact-match rule; totals and hashes are unchanged.

## [0.10.1] - 2026-10-17

See the [release notes](docs/releases/v0.10.1.md) for failure-usage and agent
attribution reporting corrections.

- Show the token subtotal recorded on failed LiteLLM rows, without changing
  recorded totals. Label gateway-recorded values as finality unknown.
- Identify the actually priced failure subset in what-if after usage, model,
  scope and price-tier checks, keeping all existing cost calculations.
- Expose missing agent attribution by call type and session. Keep JSON `agents`
  as the observed count; add coverage alongside it and show unknown in text
  when no agent identity was recorded.
- Preserve existing goldens and text for unaffected reports. Clarify the pinned
  Responses attribution and cache-detail limits in the guides.

## [0.10.0] - 2026-10-16

See the [release notes](docs/releases/v0.10.0.md) for model-mix what-if repricing.

- Re-price covered request-level usage under a proposed model mix with `what-if`.
  The checked example moves 50% of Sonnet traffic to Sol: USD 0.154400 becomes
  USD 0.109398 to 0.147974 under the measured workload assumptions.
- Support fractional moves, key/team scopes, workload mappings, dated price
  overrides and text/JSON reports, with coverage and exclusions beside costs.
- Read optional per-model summary accounting with explicit contract checks,
  replay-safe combination, missing-data exclusions and per-request price-tier
  checks. Upgrade fleetdiff before enabling producer `model_usage` output.
- Publish 468 paired fit observations, geometric-mean factors, source/license
  pins and offline reproduction. Retain the failed per-request prediction
  evidence; these are descriptive scenarios, not validated bill forecasts.

## [0.9.0] - 2026-10-15

See the [release notes](docs/releases/v0.9.0.md) for daily spend-log row coverage.

- Compare exported LiteLLM spend rows with daily request counts using optional
  `--litellm-daily-spend`. Report per-UTC-day missing rows and discrepancies
  without changing recorded token or spend totals.
- Export both tables in one read-only, repeatable-read transaction with a
  guarded private-directory helper. Pin the counter definitions and response-ID
  collision fixture to LiteLLM v1.104.0.
- Preserve existing output byte for byte when the option is absent. Extract
  shared import helpers with v0.8.0 output goldens and legacy-hash checks.

## [0.8.0] - 2026-10-11

See the [release notes](docs/releases/v0.8.0.md) for session-budget review and
the additive SQL export columns.

- Add single-period LiteLLM session-budget review: exact recorded-spend and call
  distributions, supplied-cap review, shared session counters, positive-spend
  durations, identity coverage and evidence-based clipped or possibly clipped
  sessions. Cap or single-period flags imply session grouping; files without a
  complete UTC day use their labeled observed span. Keep
  two-period comparisons unchanged.
- Add content-free `rejection_kind` and `agent_id` columns to the pinned
  LiteLLM export. Existing exports remain readable with unknown rejection
  coverage; fleetdiff v0.7.0 safely ignores the additional columns.

## [0.7.0] - 2026-10-11

See the [release notes](docs/releases/v0.7.0.md) for the SQL export default and
report upgrade.

- Compare recorded cache-read and cache-write shares and uncached input on
  LiteLLM exports, with keyed uncached-growth rankings and separate coverage.
  Include version-pinned cache and reasoning columns in the SQL export by
  default, preserving absent writer paths as NULL.
- Extend summary cache comparisons with independent read/write coverage and
  uncached totals; identify unavailable model and contributor breakdowns.
- Ship a deterministic synthetic OTLP capture for binary-only inspection,
  checked byte for byte against its generator in CI.
- Lead with significant recorded cache changes, show request coverage in words,
  and direct cache-read-share drops to provider diagnostics and route checks.

## [0.6.1] - 2026-10-11

See the [release notes](docs/releases/v0.6.1.md) for upgrade details.

- Upgrade llm-sketchkit to v0.2.3 for parser allocation limits, more efficient
  summary combination and safer secret/error diagnostics. Keep the existing
  x/text v0.41.0 normalization dependency, identities and summary wire format.
  This is the hardening backport; the v0.3.0 identity migration is separate.

## [0.6.0] - 2026-10-11

See the [release notes](docs/releases/v0.6.0.md) for the LiteLLM workflow and
upgrade details.

- Investigate LiteLLM request-level SQL exports in CSV, JSON or JSONL locally,
  without a collector. Compare complete UTC periods, recorded request count and
  size by model, and keyed contributor rankings with concentration bounds.
- Show ambiguous zero-only rows, missing/invalid usage, failures and exact
  decimal recorded spend separately. Share token accounting with `inspect` and
  preserve the collector's accounting fixtures.
- Bound file, row, field, nesting, model and duplicate-tracking resources;
  recognize daily aggregate exports and direct operators to request-level rows.
- Include native Anthropic Messages and Responses spend rows using pinned
  logging/writer probes. Lead partial comparisons with call-type coverage and
  keep unpinned token columns separate from analyzed totals.
- Accept up to 10 million rows with full-length keyed request digests in bounded
  blocks; export CSV in one database pass, with JSONL opt-in. Compute increase
  concentration before display truncation and keep tied aliases in file order.
- Show offsetting contributor changes as token totals instead of percentages
  above 100%, label the all-model decomposition, and distinguish clean usage
  from reports with listed quality issues.
- Lead the shorter README with export, verified installation and investigation;
  retain summary, trace and configuration workflows in the documentation.
- Build release artifacts with Go 1.26.9; source builds require Go 1.26 or later.
  Add signed-commit and sign-off checks, CodeQL scanning and dependency reporting.

## [0.5.0] - 2026-10-04

See the [release notes](docs/releases/v0.5.0.md) for examples and upgrade details.

- Compare recorded cached-input token shares in `investigate`, checking field
  coverage, conflicts, subset validity, and observation completeness.
- Add offline `diagnose` for the supported collector configuration and optional
  shadow configuration output, with grouped findings, YAML paths, line numbers,
  and known attribute names. JSON retains detailed checks and static limits.
- Add `scan` over retained summary windows: robust preceding baselines,
  coverage changes, bound-aware contributor and tool-error findings, persistence,
  stable finding IDs, and distinct unusual/not-ready exit codes.
- Add cache, diagnosis, and scan examples, a separate bounded archive helper,
  and packaged-command checks on all four supported platforms.
- Lead reports with compact answers, group scan findings by window, and show the
  first and last baseline window starts separately from the checked windows.

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
