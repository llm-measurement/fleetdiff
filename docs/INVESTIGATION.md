# Investigate An Application Change

Compare two windows to see how token volume changed, which contributors moved,
and which sessions deserve attention. The text report leads with a session flag
when one is available, otherwise a token-volume summary. Counts, bounds, and
coverage follow below.

Release v0.2.0 introduced `fleetdiff investigate`. Older v0.1.1 binaries only
provide `compare`. Both commands are local, read-only, and accept the same files
and validation options. One producer works; multiple stacks are optional.
Release v0.3.0 adds the user/session contributor extension and `--flag-share`
below. These require compatible attribution sketches in the input summaries.

```sh
fleetdiff investigate --before before/ --after after/ --expected app
fleetdiff investigate --before before/ --after after/ --expected app --format json
```

`--expected` lists exact summary `producer_id` values, obtained from trusted
exporter inventory. It is not a list of filenames or user/session IDs. Use the
same complete inventory in both windows, even when a file has not arrived.

JSON version 1 contains `questions` and `evidence`. Evidence uses the version 1
[comparison report](COMPARISON.md). Each question has a stable `id`, `status`,
and human-readable answer. Status is `observed`, `limited`, or `cannot_determine`.
Unknown answers are successful reports, not zero measurements; invalid inputs
still fail with no report. Consumers should use IDs and structured values rather
than parse answer prose. The internal Go implementation is not a public Go API.

The text headline summarizes displayed candidates for one measurement at a time,
preferring token-weighted session flags over attempt-weighted flags. Its share is
for one flagged session, not the sum of flagged sessions. When `--top` truncates
the candidate list, it says "shown sessions" rather than "tracked sessions".
Unequal share bounds stay a range, rounded outward. JSON fields and flag criteria
are unchanged.

## Volume

ID `volume`. Requires compatible equal-length windows, complete observation
intervals, request and input/output counters, an explicit missing-usage counter
of zero in each window, and nonzero request counts in both windows.

Let N be observed model attempts, T recorded input plus output tokens, and A=T/N.
The symmetric decomposition of T1-T0 is:

```text
request contribution = (N1 - N0) * (A1 + A0) / 2
tokens-per-request contribution = (A1 - A0) * (N1 + N0) / 2
```

The two contributions sum to the token change, up to floating-point rounding.
Neither is causal attribution or evidence of useful work. Exact integer counters
remain in `evidence`. Cache and reasoning subsets are never added again.

An attempt is a matching exported model span, including a failure or retry. It
is not a unique user request or proof the provider received it. Text reports use
"model attempts" in v0.3.0 (v0.2.0 prints "Requests"); JSON v1 retains
`requests`, `before_requests`, and the other request-named fields for compatibility.
Their meaning and arithmetic are unchanged.

If either usage field is missing, the existing summary cannot isolate the token
total from requests with both fields. Dividing by the complete-request count
would mix populations. The question therefore returns `cannot_determine`, while
retaining recorded totals and missingness. Complete upstream sampling/delivery
and provider-reported versus locally inferred usage cannot be verified here.

Rising attempts, falling recorded tokens per attempt, and rising missing usage
can suggest failures or retries worth investigating. Instrumentation loss can
look similar. This is not automatic retry-storm or runaway-agent detection, and
it does not override the refusal to split volume when usage is missing.

## Contributors

ID `contributors`. Uses the existing `top_prompts` candidate union and integer
frequency bounds. Shares are relative to each sketch's recorded configured
weight, not all application tokens. A zero denominator produces an absent share,
not zero. Displayed percentages are rounded; exact counts and denominators remain
in `evidence.concentration`. Display truncation and missing prompt keys can hide
contributors. Ordering does not prove true top-k membership. There is no entropy,
majorization test, or general recovery of previously unknown changed keys.

## User And Session Contributors

Optional `top_users` and `top_sessions` sketches describe tracked token-weighted
user and session candidates. `top_users_requests` and `top_sessions_requests`
describe request-weighted candidates instead. A request weight counts observed
model attempts, not unique end-user requests. The producer must supply compatible
accounting, hashing, and [top-k contract markers](COMPARISON.md#tracked-change);
names alone do not prove correct attribution. Question ID `users` carries user
attribution; `sessions` carries session attribution. `contributors` also accepts
`top_prompts_requests` alongside the legacy prompt sketch.

Each new contributor includes `measurement`, `weight_unit`, integer `before`,
`after`, and `delta` bounds, and available `before_share`/`after_share` bounds.
Use the measurement name with the item alias: aliases are local to each sketch
in this comparison, not durable cross-report identities. Session candidates
meeting the review rule carry `flag: "runaway_candidate"`; despite that label,
the flag is not a diagnosis.

Shares divide each item's bounds by its own window's recorded sketch weight.
They are not necessarily shares of all application tokens or attempts: missing
keys, missing usage, partial collection, and display truncation can hide activity.
A zero recorded weight gives no share, not a zero share. Hashes remain hidden
unless `--show-hashes` is supplied; aliases do not recover identities.

Session review flags use the **after-window share lower bound strictly greater
than** `--flag-share`, which defaults to `0.25` (25%). A lower bound equal to 25%,
or an upper bound above it without a lower bound above it, is not sufficient.
Flags also require complete declared observation intervals. Token-weighted flags
additionally require present request, token, and missing-usage counters with no
missing usage; request-weighted flags do not require token usage. Limited evidence
remains visible without the affected flags. Set the threshold as a finite fraction
from `0` through `1`, for example:

```sh
bin/fleetdiff investigate --before before/ --after after/ --expected app --flag-share 0.40
```

This identifies recorded concentration worth reviewing, not a runaway session,
retry loop, or cause of a token increase. No flag is not proof that every session
is below the threshold. Inspect coverage and omitted candidates too. Token and
request shares have different denominators and must not be conflated.

When a known optional sketch is absent from any snapshot, it is omitted across
both windows and named in `evidence.dropped_measurements`. Attribution present
only in one window is unknown, not a newly appearing zero-to-positive user or
session. Present but incompatible contracts remain errors.

ID `sessions` returns `cannot_determine` without session-weight attribution:
distinct MCP sessions and prompt signatures alone do not associate model tokens
with a conversation or run. The checked-in single-app examples lack session
sketches. Released v0.2.0 always gives this unknown answer for sessions and does
not recognize `--flag-share`.

## Coverage

ID `coverage` distinguishes complete declared intervals and present token fields
from partial or unknown evidence. Even complete intervals do not establish
unsampled delivery, authenticated producer identity, disjoint source traffic,
or a truthful accounting declaration. The existing comparison contract applies.

Use `--allow-partial` to inspect an incomplete observed subset, not to bypass
incompatible accounting or keys. Default reports omit raw metadata and hashes;
`--show-hashes` explicitly reveals pseudonymous, linkable item identifiers.

## Provider Origin

The provider-origin question distinguishes declared `provider_reported`, `inferred`,
`unavailable`, and `unknown` field observations using `usage_provenance.v1` counters.
Older exports and observations with unknown origin return `cannot_determine`.
For mixed old/new inputs, fleetdiff supplies unknown observations in memory while
preserving files, accounting fingerprints, and compatibility checks.

The collector excludes explicitly unavailable fields and marks those attempts
missing. Origin is an instrumenter declaration; gateways can still fill unknown
fields with zeros or estimates. Use provider records for billing reconciliation.
