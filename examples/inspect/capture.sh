#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
set -eu
umask 077
usage='Usage: sh examples/inspect/capture.sh NEW_PRIVATE_DIRECTORY [SECONDS=30]'
if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then printf '%s\n' "$usage" >&2; exit 2; fi
seconds=${2:-30}
case "$seconds" in ''|*[!0-9]*) printf '%s\n' 'Duration must be 1-300 seconds.' >&2; exit 2 ;; esac
if [ "${#seconds}" -gt 3 ] || [ "$seconds" -lt 1 ] || [ "$seconds" -gt 300 ]; then printf '%s\n' 'Duration must be 1-300 seconds.' >&2; exit 2; fi
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
command -v docker >/dev/null 2>&1 || { printf '%s\n' 'Install and start Docker first.' >&2; exit 1; }
mkdir -- "$1" || { printf '%s\n' 'Choose a new capture directory.' >&2; exit 1; }
capture=$(CDPATH= cd -- "$1" && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/fdcapture.XXXXXXXX")
name=fleetdiff-capture-$(basename "$work")
container=
cleanup() {
  if [ -n "$container" ]; then docker stop --time 10 "$container" >/dev/null 2>&1 || :; docker rm "$container" >/dev/null 2>&1 || :; fi
  chmod 600 "$capture"/* 2>/dev/null || :
  rmdir "$work" 2>/dev/null || :
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' HUP TERM
image=otel/opentelemetry-collector-contrib@sha256:fd328de2552466ad78385e1b1289c3f2402b1c45f265b252aab1955b42845ac1
container=$(docker run -d --name "$name" --user "$(id -u):$(id -g)" \
  --read-only --cap-drop=ALL --security-opt=no-new-privileges \
  --pids-limit=128 --memory=256m --log-driver=none \
  -p 127.0.0.1:14317:4317 -p 127.0.0.1:14318:4318 \
  --mount "type=bind,src=$capture,dst=/capture" \
  --mount "type=bind,src=$root/collector.yaml,dst=/etc/capture.yaml,readonly" \
  "$image" --config=/etc/capture.yaml)
printf 'Capturing for %s seconds. Send OTLP to 127.0.0.1:14317 or HTTP /v1/traces on 127.0.0.1:14318.\n' "$seconds"
printf '%s\n' 'Capture files may contain sensitive data. Ctrl-C stops capture and keeps the private files.'
sleep "$seconds"
if [ "$(docker inspect --format '{{.State.Running}}' "$container")" != true ]; then
  printf '%s\n' 'Capture collector stopped unexpectedly.' >&2; exit 1
fi
cleanup
container=
printf '%s\n' 'Capture stopped. Inspect the private directory, then delete it when finished.'
