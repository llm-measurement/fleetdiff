# Investigate An Application Change

The provider-origin question distinguishes declared `provider_reported`, `inferred`,
`unavailable`, and `unknown` field observations. It uses the collector's fixed
`usage_provenance.v1` counters. Older exports without those counters return
`cannot_determine`, as do observations with unknown origin. For mixed old/new
inputs, fleetdiff supplies unknown observations in memory, without changing files
or the base accounting fingerprint. Other compatibility checks remain strict;
partial or unrecognized-version declarations are never silently repaired.
Declared origin is not authenticated proof. The collector excludes explicitly
unavailable fields and marks requests missing. Unknown gateway-filled zeros can
still occur. Arithmetic is not a claim about provider savings.

Release v0.2.0 adds `fleetdiff investigate`. Older v0.1.1 binaries only
provide `compare`. Both commands are local, read-only, and accept the same files
and validation options. One producer works; multiple stacks are optional.

```sh
fleetdiff investigate --before before/ --after after/ --expected app
fleetdiff investigate --before before/ --after after/ --expected app --format json
```

JSON version 1 contains `questions` and `evidence`. Evidence is the unchanged
[comparison report](COMPARISON.md). Each question has a stable `id`, `status`,
and human-readable answer. Status is `observed`, `limited`, or `cannot_determine`.
Unknown answers are successful reports, not zero measurements; invalid inputs
still fail with no report. Consumers should use IDs and structured values rather
than parse answer prose. The internal Go implementation is not a public Go API.

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
"model attempts"; JSON v1 retains `requests`, `before_requests`, and the other
request-named fields for compatibility. Their meaning and arithmetic are unchanged.

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

## Sessions And Coverage

ID `sessions` currently returns `cannot_determine`: distinct MCP sessions and
prompt signatures do not associate model tokens with a conversation or run.

ID `coverage` distinguishes complete declared intervals and present token fields
from partial or unknown evidence. Even complete intervals do not establish
unsampled delivery, authenticated producer identity, disjoint source traffic,
or a truthful accounting declaration. The existing comparison contract applies.

Use `--allow-partial` to inspect an incomplete observed subset, not to bypass
incompatible accounting or keys. Default reports omit raw metadata and hashes;
`--show-hashes` explicitly reveals pseudonymous, linkable item identifiers.
