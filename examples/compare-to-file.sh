#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
set -eu
umask 077
[ "$#" = 5 ] || { echo 'usage: compare-to-file.sh BINARY BEFORE AFTER EXPECTED OUTPUT' >&2; exit 2; }
binary=$1 before=$2 after=$3 expected=$4 output=$5
case "$output" in /*) ;; *) output="./$output" ;; esac
if [ -d "$output" ] || [ -L "$output" ]; then
  echo 'output must be a file, not a directory or symbolic link' >&2; exit 2
fi
# The caller owns this directory; the temporary file is on the same filesystem.
temporary=$(mktemp "$(dirname "$output")/.fleetdiff.XXXXXXXX")
trap 'rm -f "$temporary"' EXIT HUP INT TERM
"$binary" compare --before "$before" --after "$after" --expected "$expected" --format json > "$temporary"
test -s "$temporary"
jq -e '.version == 1 and .complete_observation_intervals == true' "$temporary" >/dev/null
mv -f "$temporary" "$output"
