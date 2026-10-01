#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
set -eu
umask 077
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
usage='Usage: sh examples/investigate.sh [--two-stacks|--missing-usage|--sessions]'
if [ "$#" -gt 1 ]; then printf '%s\n' "$usage" >&2; exit 2; fi
base=examples/single-app/data
before=$base/single/before
after=$base/single/after
expected=app
producers=1
case "${1:-}" in
  '') ;;
  --two-stacks) before=$base/two-stacks/before; after=$base/two-stacks/after; expected=gateway,direct; producers=2 ;;
  --missing-usage) after=$base/missing-usage/after ;;
  --sessions) before=examples/sessions/data/before; after=examples/sessions/data/after ;;
  --help|-h) printf '%s\n' "$usage"; exit 0 ;;
  *) printf '%s\n' 'Unknown option; run sh examples/investigate.sh --help' >&2; exit 2 ;;
esac
if ! command -v go >/dev/null 2>&1; then printf '%s\n' 'Install Go from https://go.dev/dl/ and rerun.' >&2; exit 1; fi
export GOWORK=off
go build -o bin/fleetdiff ./cmd/fleetdiff
printf 'Synthetic demo: %s expected producer(s) per window (%s).\n\n' "$producers" "$expected"
if [ "${1:-}" = --sessions ]; then
  printf '%s\n\n' 'Did one session account for most of the increase? A flag is a reason to investigate, not proof of a loop.'
fi
bin/fleetdiff investigate --before "$before" --after "$after" --expected "$expected"
