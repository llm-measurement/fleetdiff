#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
# Synthetic example for the cached-token comparison included in v0.5.0.
set -eu
umask 077
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
usage='Usage: sh examples/cache.sh [--json] (source checkout)'
format=text
if [ "$#" -gt 1 ]; then printf '%s\n' "$usage" >&2; exit 2; fi
case "${1:-}" in
  '') ;;
  --json) format=json ;;
  --help|-h) printf '%s\n' "$usage"; exit 0 ;;
  *) printf '%s\n' "$usage" >&2; exit 2 ;;
esac
if ! command -v go >/dev/null 2>&1; then printf '%s\n' 'Install Go from https://go.dev/dl/ and rerun.' >&2; exit 1; fi
export GOWORK=off
tmp=$(mktemp -d "${TMPDIR:-/tmp}/fleetdiff-cache.XXXXXX")
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
go build -mod=readonly -o "$tmp/fleetdiff" ./cmd/fleetdiff
printf '%s\n' 'Synthetic cache example: 60% -> 20% recorded cached-token share.' >&2
"$tmp/fleetdiff" investigate --before examples/cache/data/before \
  --after examples/cache/data/after --expected app --format "$format"
