# Review a LiteLLM Session Budget

See which observed sessions exceed a supplied cap, address the same counter key,
or span longer than the configured counter window. This single-period review is
available in fleetdiff v0.8.0.

Export request-level rows with the matching
[LiteLLM SQL recipe](../examples/litellm-spend/README.md), then run:

```sh
fleetdiff investigate --litellm-spend spend.csv \
  --session-budget-usd 1.00
```

The default selects complete UTC days inside the file's observed time span,
excluding partial boundary days and today. When none exists, it uses the file's
observed span of request start times and labels that choice. The exclusive end
is one nanosecond after the last observed start, so every row is included.
Both choices print exact UTC boundaries and weekdays. Supply
`--period 2026-09-02` for one day, or an RFC3339 interval:

```sh
fleetdiff investigate --litellm-spend spend.csv \
  --period 2026-09-01T00:00:00Z/2026-09-04T00:00:00Z \
  --session-budget-usd 1.00 --session-counter-window 1h
```

`--period` also works without a cap to show observed distributions. Either flag
implies session grouping; an explicit different `--group-by` is rejected. `--top`
limits displayed lists, not counts or percentiles. `--format json` includes
explicit coverage and nulls for unknown rejection coverage. The existing
two-period `--before-period` / `--after-period --group-by session` comparison
keeps its key-scoped identities and output.

## What the Report Measures

The result comes first, followed by its scope, for example:

```text
3 of 4 sessions with complete recorded spend exceed the supplied USD 1.00 cap.
Grouped by session ID alone (LiteLLM v1.104.0 counter unit).
```

The report groups literal session IDs across keys, teams and agents in **one
application with one LiteLLM cache namespace**. This matches the pinned
[`max_budget_per_session_limiter.py`](https://github.com/BerriAI/litellm/blob/79645770fedc7ec2627e6468d31062f20f82aecc/litellm/proxy/hooks/max_budget_per_session_limiter.py)
counter, rather than assuming a session is a completed task run. Session IDs are
keyed locally before aggregation; only aliases (or opt-in keyed hashes) appear.

- Spend is the exact decimal sum of recorded `spend` values, in USD. Sessions
  with missing or invalid spend have a known subtotal and stay out of the
  complete-spend distribution. Calls have their own independent sample count.
- Percentiles use exact nearest rank, with `n` printed. They describe observed
  totals, including sessions with recorded budget or iteration rejections.
  Rejection-only sessions are separate from model-call distributions.
- Cap review lists complete totals strictly greater than the supplied cap, and
  incomplete subtotals already above it. An equal total is not an exceedance.
  The supplied cap is a review value, not proof of each agent's configuration.
- Rejection classification comes from the content-free SQL `rejection_kind`
  column. Older exports work with unknown coverage. A null classification means
  the pinned SQL did not recognize a rejection; absence of a logged rejection
  does not establish that none occurred. Raw error messages are never exported.
- Duration uses the first and last positive-spend request start times. A span
  longer than `--session-counter-window` is a duration fact, not a detected
  reset. The comparison defaults to 3,600 seconds.
- Counter-key reuse lists count distinct nonempty key, team and agent IDs present
  in the export. Shared storage and agent budget configuration determine actual
  tracking. Missing identity fields cannot establish separate ownership.
- Rows without a session ID keep their known spend subtotal and missing/invalid
  spend counts separately, outside session distributions and cap review.
- A session is **clipped** when its ID also occurs outside the selected period
  in the file. Otherwise it is **possibly clipped** only when activity is within
  one counter window of an edge with no file rows beyond that edge. These are
  separate `clipped` and `possibly_clipped` fields in JSON, with report counts.
  A short observed-span capture can still leave several sessions possibly clipped.
  If most sessions have one call, check propagation: a
  generated request-level ID can make every request look like a session.

## Context

Review last-call or parallel-call overshoot, counter-window boundaries, agent
budget configuration and failure logging when inspecting above-cap sessions.
This is an observed-record review, not an enforcement verdict or a forecast.
Recorded spend is source-reported accounting, not an independently verified
invoice. Export completeness and completed task boundaries remain external to
the file.

The parser retains the [spend reader limits](../examples/litellm-spend/README.md). Exact review also
limits a selected period to 250,000 session IDs and 1,000,000 distinct
session/key, session/team and session/agent memberships. Select a shorter period
when a limit is reached; there is no silent sampling or identity eviction.
Outside-period clipping checks reuse the selected-session digest map in a
streaming pass, keeping those same caps. The input is checked for changes
between passes.
