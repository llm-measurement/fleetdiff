#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
set -eu
umask 077
archive=${1:?supply a Linux release archive}
platform=${2:?supply linux/amd64 or linux/arm64}
case "$platform" in linux/amd64|linux/arm64) ;; *) exit 2 ;; esac
work=$(mktemp -d)
image="fleetdiff-restricted-test-$$"
cleanup() { docker image rm "$image" >/dev/null 2>&1 || true; rm -rf "$work"; }
trap cleanup EXIT HUP INT TERM
tar -xzf "$archive" -C "$work" fleetdiff
chmod 755 "$work/fleetdiff"
cp scripts/restricted.Dockerfile "$work/Dockerfile"
docker build --network none --platform "$platform" -t "$image" "$work" >/dev/null
docker run --rm --platform "$platform" --network none --read-only --user 65532:65532 \
  --cap-drop ALL --security-opt no-new-privileges --memory 256m --cpus 1 --pids-limit 64 \
  --mount "type=bind,src=$(pwd)/examples/two-systems/data,dst=/data,readonly" \
  "$image" compare --before /data/before --after /data/after --expected owned,partner --format json > "$work/report.json"
test -s "$work/report.json"
jq -e '.version == 1 and .complete_observation_intervals == true and
    ([.counters[] | select(.name == "requests") | .after] == [10])' "$work/report.json" >/dev/null
printf 'Restricted runtime passed: %s\n' "$platform"
