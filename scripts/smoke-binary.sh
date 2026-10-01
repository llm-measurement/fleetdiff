#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
set -eu
umask 077
binary=${1:?supply a built binary}
report=$(mktemp)
trap 'rm -f "$report"' EXIT HUP INT TERM
"$binary" --version
"$binary" compare --before examples/two-systems/data/before \
  --after examples/two-systems/data/after --expected owned,partner --format json > "$report"
test -s "$report"
jq -e '.version == 1 and .complete_observation_intervals == true and
    ([.counters[] | select(.name == "requests") | .before == 6 and .after == 10] == [true]) and
    ([.concentration[] | select(.name == "top_prompts") | .before_weight == 1200 and .after_weight == 1800] == [true])' "$report" >/dev/null
"$binary" investigate --before examples/single-app/data/single/before \
  --after examples/single-app/data/single/after --expected app --format json > "$report"
jq -e '.version == 1 and
    ([.questions[] | select(.id == "volume") | .status == "observed" and
      .volume.before_reported_tokens == 200 and .volume.after_reported_tokens == 600 and
      .volume.request_count_contribution_tokens == 150 and
      .volume.tokens_per_request_contribution_tokens == 250] == [true]) and
    ([.questions[] | select(.id == "usage_source") | .status == "cannot_determine"] == [true])' "$report" >/dev/null
"$binary" investigate --before examples/single-app/data/single/before \
  --after examples/single-app/data/missing-usage/after --expected app --format json > "$report"
jq -e '([.questions[] | select(.id == "volume") | .status == "cannot_determine"] == [true])' "$report" >/dev/null
"$binary" investigate --before examples/sessions/data/before \
  --after examples/sessions/data/after --expected app --format json > "$report"
jq -e '([.questions[] | select(.id == "volume") | .status == "observed" and
      .volume.before_reported_tokens == 400 and .volume.after_reported_tokens == 3300] == [true]) and
    ([.questions[] | select(.id == "sessions") | .contributors[] | select(.flag == "runaway_candidate") |
      .after.lower == 3000 and .after.upper == 3000] == [true]) and
    ([.questions[] | select(.id == "users") | .contributors[] | select(.after.lower == 3000)] | length == 1) and
    ([.questions[] | select(.id == "usage_source") | .status == "cannot_determine"] == [true])' "$report" >/dev/null
