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
Release v0.3.1 adds the concise text layout shown in the examples and whole-token
contributions. The JSON version and calculated values remain unchanged.

This checkout also adds question ID `cache`: recorded cached-input token share
before and after, with coverage and quality checks. It is not included in the
v0.4.0 binary. See [cached-token comparison](CACHE.md). Consumers should select
questions by ID rather than list position and tolerate additive questions.

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
Exact integer counters remain in `evidence`. Cache and reasoning tokens stay
within their input/output totals.
Text reports round contributions to whole tokens; JSON retains the calculated
precision. Rounded contributions can differ from the total change by one token.

An attempt is a matching exported model span, including a failure or retry.
Text reports use "model attempts" in v0.3.0 (v0.2.0 prints "Requests"); JSON v1 retains
`requests`, `before_requests`, and the other request-named fields for compatibility.
Their meaning and arithmetic are unchanged.

If either usage field is missing, the existing summary cannot isolate the token
total from requests with both fields. Dividing by the complete-request count
would mix populations. The question therefore returns `cannot_determine`, while
retaining recorded totals and missingness.

Rising attempts, falling recorded tokens per attempt, and rising missing usage
can suggest failures or retries worth investigating. Check traces and
instrumentation coverage to distinguish them; the volume split requires complete
usage.

## Contributors

ID `contributors`. Uses the existing `top_prompts` candidate union and integer
frequency bounds. Shares use each sketch's recorded configured weight.
Displayed percentages are rounded; exact counts and denominators remain in
`evidence.concentration`. Increase `--top` to show more tracked candidates.

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
in this comparison. Session candidates meeting the review rule print
`flagged for review`; JSON retains `flag: "runaway_candidate"`.

Shares divide each item's bounds by its own window's recorded sketch weight.
Token and request weights have separate denominators. A zero recorded weight
gives an absent share. Hashes remain hidden unless `--show-hashes` is supplied.

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

Use flags to choose sessions for trace inspection. Check coverage and omitted
candidates alongside the visible rows.

When a known optional sketch is absent from any snapshot, it is omitted across
both windows and named in `evidence.dropped_measurements`. Attribution present
only in one window is unknown, not a newly appearing zero-to-positive user or
session. Present but incompatible contracts remain errors.

ID `sessions` needs session-weight attribution linking model activity to a session.
Try `sh examples/investigate.sh --sessions` for a complete synthetic example.
The basic single-app fixture leaves that question open; v0.2.0 predates these
rankings and `--flag-share`.

## Coverage

ID `coverage` distinguishes complete declared intervals and present token fields
from partial or unknown evidence. The text report prints coverage once; JSON
retains each question's status and the underlying counters.

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
missing. The text report collects unanswered questions into a closing guidance
line naming the additional measurements needed.

## What The Numbers Mean

- Shares cover weight attributed to each configured key. Missing keys, partial
  collection, and display truncation can hide activity; a zero denominator gives
  an unknown share. Tracked candidates describe the head of the distribution,
  not every key or a guaranteed rank ordering.
- A review flag identifies concentration above the chosen lower-bound threshold,
  not proof of a loop. Unflagged or undisplayed sessions still warrant attention
  when coverage is incomplete. Use traces to investigate causes.
- Model attempts include exported failures and retries, not unique user requests
  or proof of provider receipt. Complete declared intervals do not establish
  complete upstream delivery. Usage origin is an instrumenter declaration;
  undeclared counts may include gateway zeros or estimates.
- Aliases belong to one measurement in one comparison. For sharing and producer
  trust, see [export privacy](FAQ.md#are-exports-and-reports-safe-to-share) and
  [Safe Use](../SECURITY.md#safe-use). For missing data, billing, and causality,
  see [Reading The Results](../README.md#reading-the-results).
