#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
set -eu
umask 077
binary=${1:?supply a built binary}
report=$(mktemp)
trap 'rm -f "$report"' EXIT HUP INT TERM
"$binary" --version
"$binary" inspect --format json testdata/inspect-contract/v1/litellm-normal-no-usage-stock.json > "$report"
jq -e '.schema == "fleetdiff-inspect/v1" and
    .metrics.gen_ai_sketch_requests_total == 1 and
    .metrics.gen_ai_sketch_missing_token_usage_total == 0 and
    .usage_provenance["input/unknown"] == 1' "$report" >/dev/null
"$binary" inspect --format json testdata/inspect-contract/v1/litellm-normal-no-usage-provenance.json > "$report"
jq -e '.metrics.gen_ai_sketch_requests_total == 1 and
    .metrics.gen_ai_sketch_missing_token_usage_total == 1 and
    .usage_provenance["input/unavailable"] == 1' "$report" >/dev/null
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
"$binary" investigate --before examples/sessions/data/before \
  --after examples/sessions/data/after --expected app > "$report"
grep -Fx '1 of 8 tracked sessions flagged for review: 90.91% of attributed tokens.' "$report"
grep -Fx '  +1592 tokens from attempt count; +1308 tokens from tokens per attempt.' "$report"
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
