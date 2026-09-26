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
