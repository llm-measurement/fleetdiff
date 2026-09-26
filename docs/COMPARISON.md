# Comparison Contract v1

## Input And Output

The command consumes canonical [sketchkit summary v1](https://github.com/llm-measurement/llm-sketchkit/blob/v0.2.0/spec/summary.md)
files. It combines each window independently using `summary.Combine`, then checks
cross-window compatibility using `summary.Compatible`. Each frequent-items state
is checked for totals consistent with its retained bounds before combination,
including replayed snapshots and measurements not displayed. Before must end at or before
after begins; durations and measurement settings must match.

Only cumulative snapshots are accepted. Replays do not add counts. The highest
sequence per producer/process epoch replaces earlier snapshots, subject to the
summary contract's checks. Nonoverlapping restart epochs contribute separately;
conflicts and overlapping epochs are rejected. Windows may be historical: this
tool does not impose a wall-clock freshness cutoff or feed an automatic controller.

The same explicit expected producer set applies to both windows. Missing sources
and partial collection intervals require `--allow-partial`. That option permits an
observed comparison, not a claim of complete workload change. Empty inputs fail.

Report JSON has version `1` and contains only:

- Selected window times, durations, snapshot counts, and coverage aliases.
- Allowlisted observed counters with signed arithmetic deltas and declared units.
- Available request/missing-usage counts; absent coverage fields are not inferred.
- Allowlisted distinct estimates and nominal HLL RSE (`1.04 / sqrt(2^p)`, using
  normal precision even for sparse state, not a per-input error guarantee).
- Configured frequent-item weight totals, per-window error, candidate counts,
  intervals, and tracked directions. Hashes appear only with `--show-hashes`.
- Counts of omitted measurement names and explicit interpretation notes.

Optional output measurements are not synthesized as zero. Unknown measurements
remain subject to input compatibility checks but their names and values are not
exported. Collector token-field diagnostic counters outside the allowlist are
included in the omitted count; use the collector metrics for their detail.

## Tracked Change

All candidates from each no-false-negatives frequent-item query are unioned before
output truncation. The default is 20 displayed rows; the maximum is 100. For each
candidate, query both sketches' lower and upper bounds, including untracked bounds.
Subtract endpoints to obtain the deterministic interval for observed weight change.

An interval entirely above zero is `increased`; entirely below zero is `decreased`;
exactly `[0,0]` is `unchanged`; all other intervals are `uncertain`. This direction
concerns observed keyed weight, not population-wide statistical or causal change.

A key outside the full candidate union has a change within
`[-before.max_error, after.max_error]`. Keys omitted only by the display limit do
not inherit that smaller untracked bound. Sorting uses descending maximum absolute
delta endpoint and then ascending hash for deterministic ties. Overlapping
intervals do not establish true rank order.

Different models' token counts are not normalized compute. `top_prompts` is usually
token-weighted by the connector, but fleetdiff does not infer units from a payload
name or reinterpret the accounting fingerprint. The report preserves the generic
`configured-weight` label. It is not a billing ledger or invoice reconciliation.

## Trust And Effects

The runtime reads files and writes stdout/stderr only. It needs no hashing secret
and performs no networking, remote lookup, policy decision, or enforcement.
Use authenticated transport outside the command. Summary identity, key identity,
accounting, and disjoint event ownership are declarations, not authenticated proof.
Do not use untrusted metadata to grant authority.

Metadata and arbitrary measurement names can contain sensitive data. The report
uses reviewed names and generated aliases rather than reflecting input. Error
messages do not quote command arguments, filenames, or malformed JSON. Opt-in
hashes are pseudonyms, not recovered prompts. Frequency patterns may still be
identifying; access-control inputs and reports appropriately.

The input limits bound encoded bytes, file count, and directory entries, not a
promise of constant CPU time or a measured maximum RSS. Parsing and canonical
validation also allocate decoded sketch state. Linux and macOS are the initial
supported operating systems; regular-file opening uses nonblocking/no-follow
flags and a confined directory root.

Exit codes: `0` means a report or help was written; `1` means input, comparison,
or output failure; `2` means invalid command options. An explicitly allowed
partial report exits `0` and carries its partial state. Output write failure can
leave bytes already accepted by stdout; input/validation errors emit no report.

## JSON Compatibility

Use `--format json` for automation. A report is one JSON object followed by a
newline; diagnostics are on stderr. Top-level `version` identifies the report
contract, independently of the binary version and input summary version.

Within report v1, existing field meanings and types are preserved. Consumers
should ignore added fields, handle new allowlisted measurement names, and reject
unsupported major report versions. Removing or reinterpreting a field requires a
new report version. Text layout, diagnostic wording, ordering of explanatory
notes, and display rankings are not machine interfaces. Numeric counters and
bounds can exceed JavaScript's exact integer range: use an integer-preserving
JSON decoder rather than rounding them through floating point.

`counters`, `distinct`, `concentration`, and coverage alias lists are arrays even
when empty. `token_coverage` is absent when its input counters are unavailable.
`hash` is absent unless requested. Missing measurements are not implicit zeros.
Use each measurement's `name` rather than its array position. Aliases identify
only this comparison; they are not durable cross-report entity IDs.

The `complete_observation_intervals` boolean refers to the expected producer set
and declared observation intervals, not the presence of every upstream event or
token field. Check `token_coverage` separately. The report does not contain an
authentication verdict or permission to enforce a policy.
