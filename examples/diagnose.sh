#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
set -eu
umask 077
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
case "${1:-}" in
  '') ;;
  --help|-h) printf '%s\n' 'Usage: sh examples/diagnose.sh'; exit 0 ;;
  *) printf '%s\n' 'Unknown option; run sh examples/diagnose.sh --help' >&2; exit 2 ;;
esac
if [ "$#" -gt 1 ]; then printf '%s\n' 'Supply no options.' >&2; exit 2; fi
if ! command -v go >/dev/null 2>&1; then printf '%s\n' 'Install Go from https://go.dev/dl/ and rerun.' >&2; exit 1; fi
work=$(mktemp -d "${TMPDIR:-/tmp}/fleetdiff-diagnose.XXXXXXXX")
trap 'rm -rf "$work"' EXIT HUP INT TERM
export GOWORK=off
cd "$root"
go build -o "$work/fleetdiff" ./cmd/fleetdiff
check() {
  want=$1
  name=$2
  status=0
  printf '\n%s\n' "$name.yaml:"
  "$work/fleetdiff" diagnose - < "examples/diagnose/$name.yaml" || status=$?
  if [ "$status" -ne "$want" ]; then printf '%s\n' 'Unexpected diagnosis status.' >&2; exit 1; fi
}
printf '%s\n' 'Synthetic safe, blocking, and indeterminate configuration fixtures:'
check 0 safe
check 3 unsafe
check 4 unsupported
"$work/fleetdiff" diagnose --shadow-config - < examples/diagnose/safe.yaml > "$work/shadow.yaml" 2> "$work/shadow-report.txt"
test -s "$work/shadow.yaml"
printf '\n%s\n' 'All three checks returned the expected exit codes; the standalone shadow proposal was also generated.' 'Next: run fleetdiff diagnose --shadow-config collector.yaml to review a separate shadow configuration.'
