#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
set -eu
umask 077
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
case "${1:-}" in
  '') quiet= ;;
  --quiet) quiet=--quiet ;;
  --help|-h) printf '%s\n' 'Usage: sh examples/scan.sh [--quiet]'; exit 0 ;;
  *) printf '%s\n' 'Unknown option; run sh examples/scan.sh --help' >&2; exit 2 ;;
esac
if [ "$#" -gt 1 ]; then printf '%s\n' 'Supply at most one option.' >&2; exit 2; fi
if ! command -v go >/dev/null 2>&1; then printf '%s\n' 'Install Go from https://go.dev/dl/ and rerun.' >&2; exit 1; fi
work=$(mktemp -d "${TMPDIR:-/tmp}/fleetdiff-scan.XXXXXXXX")
trap 'rm -rf "$work"' EXIT HUP INT TERM
export GOWORK=off
cd "$root"
go build -o "$work/fleetdiff" ./cmd/fleetdiff
go run ./examples/scan --out "$work/windows" ${quiet:+"$quiet"}
printf 'Synthetic history: 30 one-minute windows, judged at a fixed reference time.\n\n'
code=0
"$work/fleetdiff" scan "$work/windows" --expected app --recent 2 --as-of 2026-10-04T00:30:00Z || code=$?
if [ "$code" -ne 0 ] && [ "$code" -ne 3 ]; then exit "$code"; fi
if [ -z "$quiet" ] && [ "$code" -ne 3 ]; then printf '%s\n' 'Expected unusual windows were not found.' >&2; exit 1; fi
if [ -n "$quiet" ] && [ "$code" -ne 0 ]; then printf '%s\n' 'Quiet windows were unexpectedly flagged.' >&2; exit 1; fi
