<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Code authors: Vijay and Codex -->

# Watch For Unusual Windows

`fleetdiff scan` checks recent summary windows against preceding history. It
names the change, shows its typical value, and identifies leading contributors
when the exported sketches support that question. Start with one application.

Included in fleetdiff v0.5.0. Use the [verified release binary](OPERATIONS.md#install-and-verify)
or build with `go build -o bin/fleetdiff ./cmd/fleetdiff`.

```sh
sh examples/scan.sh
sh examples/scan.sh --quiet
fleetdiff scan ./archive --expected app
fleetdiff scan ./archive --expected app --recent 10 --persist 2 --format json
```

The demo generates 30 synthetic one-minute windows without Docker or model calls.
It plants increased tokens per attempt, a concentrated session, missing usage,
and tool errors. The quiet variant must not flag. The same assertions run in CI.

Text groups findings under each window's timestamp. For example:

```text
2026-10-04T00:28:00Z
  session-1 now holds 62% of tokens (newly prominent in this scan).
```

Here, tokens means the ranking's recorded token weight, not all application
traffic. "Newly prominent" describes a change in share during this scan; it does
not establish a new session or identity. An untracked baseline key can still have
nonzero weight within the sketch's bounds. Interval bounds and unavailable
measurements remain explicit. One short `Notes:` line summarizes the text report;
JSON retains all notes and detailed evidence.

## Signals

| Signal | What is compared |
| --- | --- |
| Model attempts | Volume, up or down, with a minimum-volume guard |
| Reported tokens per attempt | Input plus output divided by attempts; complete usage is required in every compared window |
| Missing-usage share | Missing usage divided by attempts; an increase is a coverage change, never savings |
| Leading session or user share | Current lower share against baseline upper shares, with a share floor |
| Newly prominent contributors | Each current user/session/prompt candidate against its per-key baseline upper bounds, including the untracked-key bound |
| Tool-error signature surge | Error-event lower count against per-key baseline upper counts |

Shares use attributed weight, not all application traffic. Token and attempt
rankings remain separate. Legacy `top_prompts` uses its configured weight.
Newly prominent means a bounded change in share, not proof that a key never
existed. A leading-share finding is folded into a newly prominent finding for
the same key and measurement to avoid duplicate alerts. Optional rankings that
were not configured are listed as `not_configured` in JSON.

## Baseline And Thresholds

The default baseline is 24 equal-duration windows immediately before the recent
period and any persistence lookback. At least six are required. If fewer than
24 are available, the report states how many were used. The baseline is fixed
for that invocation: no judged window contributes to its own baseline.
`--baseline 1h` is also accepted when it is a whole number of summary windows.

The displayed range lists the **first and last baseline window starts**. In the
default demo, the 24 baseline windows start at **00:04 through 00:27 UTC on
2026-10-04**, before the first judged window at **00:28**. JSON's
`baseline_end_unix_nano` remains end-exclusive: **00:28**, not 00:27. Formatting
does not change window selection. Recent windows, open windows, and older windows
outside the selected baseline do not contribute to its statistics.

The typical value is the median. The robust score divides the change by
`max(1.4826 * MAD, scale floor)`. Floors are one attempt, one token per attempt,
and one percentage point for shares. This keeps flat baselines finite.
The default score threshold is 4 and minimum relative change is 50%.

Defaults also require 100 model attempts, a 25% contributor share, a
10-percentage-point contributor increase, and a 5-percentage-point missing-usage
increase. Tool-error surges require a lower-bound increase greater than 10 events.
Volume drops can still flag when the current count falls below 100, provided the
baseline clears the guard. Relative change from zero uses the absolute and
robust-score guards, not division by zero.

Sketch findings must clear the maximum relevant baseline upper bound as well
as the statistical threshold. No flag is based on a point estimate alone.
Run `fleetdiff scan --help` for the exposed threshold settings.

`--persist 2` requires the same signal, measurement, direction, and contributor
to be unusual in two consecutive windows. Quiet, incomplete, or missing windows
cannot bridge the streak. This reduces transient flags; it does not store an
alert state or provide separate trigger/recovery thresholds.

## History And Freshness

The collector replaces cumulative snapshots during a window and deletes exports
outside its retention. **Do not use `cp -n` to freeze the first snapshot.** Use
the [separate archive helper](SUMMARY_ARCHIVE.md), which refreshes closed-window
snapshots, or an existing archive with equivalent sequence and atomic-write checks.
The scanner itself never writes files.

Only closed windows are judged. Missing producers, partial baseline observations,
gaps, future emission times, and insufficient history produce an explicit
unavailable result. The scanner never silently rolls back to an older healthy
window. By default the latest closed window must be at most three window
durations old; `--max-age` changes that tolerance.

For a deliberately historical comparison, use `--as-of`:

```sh
bin/fleetdiff scan ./archive --expected app --as-of 2026-10-04T00:30:00Z
```

Every supplied snapshot is validated, including older or superseded snapshots.
Scope, duration, hashing, accounting, and measurement contracts must agree. An
incompatible series is an error; select a compatible archive interval instead.
Latest sequence per producer/epoch is selected without double-counting replay.
Different epochs are combined using their observation intervals.

## Automation

| Exit | Meaning |
| --- | --- |
| 0 | Available signals evaluated; no persistent unusual findings |
| 3 | At least one unusual finding, possibly alongside limited signals |
| 4 | No findings, but history or relevant coverage prevents a quiet verdict |
| 1 | Input, compatibility, resource-limit, or output error |
| 2 | Invalid command options |

Use a wrapper from cron or your scheduler; cron does not turn a nonzero exit
status into a notification by itself. For example, call this wrapper once a
minute after the archive update:

```sh
code=0
fleetdiff scan ./archive --expected app --persist 2 --format json || code=$?
case "$code" in
  0) ;;
  3) printf '%s\n' 'Unusual windows: send this report to your alert handler.' >&2 ;;
  4) printf '%s\n' 'Collection or history needs attention.' >&2 ;;
  *) printf '%s\n' 'Scan failed; inspect the diagnostic.' >&2 ;;
esac
exit "$code"
```

JSON uses `fleetdiff-scan/v1`, with per-window signal status, baseline statistics,
thresholds, bounded findings, and stable finding IDs. Deduplicate IDs in your
existing alerting system to avoid notifying repeatedly about the same window.
For sketch findings, `weight_bounds` and `total_weight` retain exact integer
evidence; floating-point `current` shares are presentation values. Integer
uncertainty remains visible even if the floating-point endpoints round together.
Aliases are local to a report. Raw metadata and hashes stay hidden by default;
`--show-hashes` explicitly exposes pseudonymous identifiers.

## Limits

This is a recent-history heuristic, not a seasonal model or a calibrated
false-alarm probability. A flag directs investigation; it does not prove a loop,
causality, delivery completeness, or an invoice discrepancy. Usage origin remains
an instrumentation declaration. Detection delay depends on window closure,
export, archiving, polling, and persistence; no fixed one-minute promise is made.

Inputs remain capped at 512 files, 1,024 directory entries, and 32 MiB in total.
The baseline supports 6-384 windows, reports up to 128 recent windows, and uses
at most 32 persistence windows. A sketch may contribute at most 4,096 candidates;
the entire scan is capped at two million candidate/baseline comparisons. Limits
fail explicitly instead of silently truncating history. `--top` limits displayed
findings only, not detection. Scope long histories to the period being checked.
