#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
set -u
umask 077
# Building is an explicit setup step, not an implicit cron/cache write.
if [ "$#" = 1 ] && { [ "$1" = --help ] || [ "$1" = -h ]; }; then
  printf '%s\n' 'Usage: sh examples/archive.sh SOURCE NEW_OR_PRIVATE_ARCHIVE [--keep-windows 64]' 'Build the local helper first; see docs/SUMMARY_ARCHIVE.md.'
  exit 0
fi
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." 2>/dev/null && pwd -P) || {
  printf '%s\n' 'archive: cannot locate local helper' >&2
  exit 1
}
helper=${FLEETDIFF_ARCHIVE_HELPER:-"$root/bin/fleetdiff-archive"}
# Suppress shell/loader diagnostics too: they can include private paths.
(exec "$helper" "$@") 2>/dev/null
status=$?
if [ "$status" != 0 ]; then
  printf '%s\n' 'archive: helper failed; check local setup, inputs, permissions, and budgets' >&2
fi
exit "$status"
