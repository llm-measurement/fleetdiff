#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
set -eu
set -C
umask 077
if [ "$#" -lt 3 ] || [ "$#" -gt 4 ]; then
  printf '%s\n' 'Usage: sh export-coverage.sh NEW_DIRECTORY START_UTC END_UTC [PG_SERVICE]' >&2
  exit 2
fi
directory=$1
start=$2
end=$3
service=${4:-litellm_readonly}
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
if ! command -v psql >/dev/null 2>&1; then
  printf '%s\n' 'Install psql before exporting.' >&2
  exit 1
fi
# A new private directory and exclusive opens prevent overwriting existing exports.
# psql receives handles, not caller-selected paths that could alias one another.
if ! mkdir -m 700 -- "$directory" 2>/dev/null; then
  printf '%s\n' 'Choose a new output directory with an existing private parent.' >&2
  exit 1
fi
exec 3>"$directory/spend.csv" 4>"$directory/daily.csv"
if ! psql "service=$service" -Xq -v ON_ERROR_STOP=1 \
    -v "start_utc=$start" -v "end_utc=$end" \
    -v csv_file=/dev/fd/3 -v daily_csv_file=/dev/fd/4 \
    -f "$script_dir/export.sql" </dev/null \
    >"$directory/private-psql.out" 2>"$directory/private-psql.err"; then
  printf '%s\n' 'Coverage export failed; inspect private-psql.err in the new output directory.' >&2
  exit 1
fi
exec 3>&- 4>&-
printf '%s\n' 'Exported spend.csv and daily.csv in the new private output directory.'
