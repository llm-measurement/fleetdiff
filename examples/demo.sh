#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
set -eu
umask 077

usage() {
  printf '%s\n' 'Usage: sh examples/demo.sh [--live]' \
    'Default: compare the included sample files. Requires Go 1.25 or 1.26.' \
    '--live: first generate exports with two local collectors. Also requires Docker.'
}

if [ "$#" -gt 1 ]; then usage >&2; exit 2; fi
case "${1:-}" in
  '') live=false ;;
  --live) live=true ;;
  --help|-h) usage; exit 0 ;;
  *) usage >&2; exit 2 ;;
esac

if ! command -v go >/dev/null 2>&1; then
  printf '%s\n' 'Install Go 1.25 or 1.26 from https://go.dev/dl/, then run this command again.' >&2
  exit 1
fi
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
export GOWORK=off

if [ "$live" = true ]; then
  if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
    printf '%s\n' 'Start Docker and try again, or omit --live to use the included sample files.' >&2
    exit 1
  fi
  image=$(cat examples/two-operators/image.txt)
  if ! docker image inspect "$image" >/dev/null 2>&1; then
    printf '%s\n' 'Downloading the pinned Collector image (first live run only)...'
    docker pull "$image"
  fi
fi

printf '%s\n' 'Building fleetdiff (the first run may download Go dependencies)...'
go build -o bin/fleetdiff ./cmd/fleetdiff
mkdir -p .cache
run_dir=$(mktemp -d .cache/demo.XXXXXX)
if [ "$live" = true ]; then
  printf '%s\n' 'Starting two collectors. Allow about a minute; keep this machine awake.'
  go run ./examples/two-operators -out "$run_dir/collectors"
  go run ./examples/walkthrough -live "$run_dir/collectors" -out "$run_dir/reports"
else
  go run ./examples/walkthrough -out "$run_dir/reports"
fi
printf '\nFull report: cat "%s/%s/reports/comparison.txt"\n' "$root" "$run_dir"
printf 'JSON report: %s/%s/reports/comparison.json\n' "$root" "$run_dir"
printf '%s\n' 'To compare your own exports, see the README section "Use Your Own Data".'
