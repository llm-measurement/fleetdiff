#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
set -eu
umask 077
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
case "${1:-}" in
  '') variant=stock ;;
  --provenance) variant=provenance ;;
  --help|-h) printf '%s\n' 'Usage: sh examples/inspect.sh [--provenance]'; exit 0 ;;
  *) printf '%s\n' 'Unknown option; run sh examples/inspect.sh --help' >&2; exit 2 ;;
esac
if [ "$#" -gt 1 ]; then printf '%s\n' 'Supply at most one option.' >&2; exit 2; fi
if ! command -v go >/dev/null 2>&1; then printf '%s\n' 'Install Go from https://go.dev/dl/ and rerun.' >&2; exit 1; fi
work=$(mktemp -d "${TMPDIR:-/tmp}/fleetdiff-inspect.XXXXXXXX")
trap 'rm -rf "$work"' EXIT HUP INT TERM
export GOWORK=off
cd "$root"
go build -o "$work/fleetdiff" ./cmd/fleetdiff
go run ./examples/inspect --variant "$variant" > "$work/traces.json"
printf 'Synthetic capture: four model attempts, one tool, and one agent wrapper.\n\n'
"$work/fleetdiff" inspect "$work/traces.json"
