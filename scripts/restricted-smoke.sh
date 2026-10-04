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
docker run --rm --platform "$platform" --network none --read-only --user 65532:65532 \
  --cap-drop ALL --security-opt no-new-privileges --memory 256m --cpus 1 --pids-limit 64 \
  --mount "type=bind,src=$(pwd)/testdata/inspect-contract/v1,dst=/data,readonly" \
  "$image" inspect --format json /data/litellm-normal-no-usage-provenance.json > "$work/inspect.json"
jq -e '.schema == "fleetdiff-inspect/v1" and
    .metrics.gen_ai_sketch_requests_total == 1 and
    .metrics.gen_ai_sketch_missing_token_usage_total == 1' "$work/inspect.json" >/dev/null
printf 'Restricted offline inspection passed: %s\n' "$platform"
docker run --rm --platform "$platform" --network none --read-only --user 65532:65532 \
  --cap-drop ALL --security-opt no-new-privileges --memory 256m --cpus 1 --pids-limit 64 \
  --mount "type=bind,src=$(pwd)/examples/diagnose,dst=/data,readonly" \
  "$image" diagnose --format json /data/safe.yaml > "$work/diagnose.json"
jq -e '.status == "supported_safe" and .configuration_parsed == true' "$work/diagnose.json" >/dev/null
printf 'Restricted offline diagnosis passed: %s\n' "$platform"
go run ./examples/scan --out "$work/windows"
# These are generated synthetic fixtures, made readable for the non-root container.
chmod 755 "$work/windows"
chmod 644 "$work/windows/"*.json
status=0
docker run --rm --platform "$platform" --network none --read-only --user 65532:65532 \
  --cap-drop ALL --security-opt no-new-privileges --memory 256m --cpus 1 --pids-limit 64 \
  --mount "type=bind,src=$work/windows,dst=/data,readonly" \
  "$image" scan /data --expected app --recent 2 --as-of 2026-10-04T00:30:00Z --format json > "$work/scan.json" || status=$?
test "$status" -eq 3
jq -e '.schema == "fleetdiff-scan/v1" and .unusual_windows == 2' "$work/scan.json" >/dev/null
printf 'Restricted offline scan passed: %s\n' "$platform"
