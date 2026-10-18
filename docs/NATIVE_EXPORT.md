# Native LiteLLM Export

**Get a spend investigation directly from your existing LiteLLM database.**
fleetdiff exports a private read-only snapshot, analyzes it locally, and removes
the temporary files unless you ask to keep them. No `psql` installation is needed
for the native path.

This is an **unreleased v0.12 candidate**. Build a reviewed candidate checkout
using the [development setup](OPERATIONS.md#native-candidate-build-development-only).
The released v0.11.0 installer does not include these commands; use the
[manual SQL export](../examples/litellm-spend/README.md#export-request-level-rows)
with that binary. The examples below run from the candidate checkout.

## First Report

After the administrator setup below, set `DATABASE_URL_FILE` to the approved
credential file's path. Leave `FLEETDIFF_LITELLM_DATABASE_URL` unset when using it:

```sh
: "${DATABASE_URL_FILE:?Set the path to your approved database URL file}"
./fleetdiff-install/fleetdiff investigate --litellm-db \
  --database-url-file "$DATABASE_URL_FILE" --group-by key
```

The default compares the last 14 complete UTC days as two adjacent seven-day
periods, ending at today's UTC midnight. This is based on the clock, not the
first and last rows found. Empty or sparse logs do not establish complete coverage.
Add `--daily` to compare against daily counters, or `--routing-audit` to review
the models serving each original route.

For chosen comparison windows, use both `--before-period` and `--after-period`
with UTC dates or RFC3339 `start/end` intervals. They must be complete, ordered,
nonoverlapping and equal-duration. Single-period session review uses `--period`
and/or `--session-budget-usd`, as in the [session guide](SESSION_BUDGET.md).

## Credentials

Choose exactly one source:

- `--database-url-file FILE`: an existing regular file owned by the effective
  user, with mode exactly 0600 and no symlink path components. Have your approved
  secret-delivery process provision it in a private directory outside the checkout.
  `DATABASE_URL_FILE` in the examples is only a shell variable holding that path.
- `FLEETDIFF_LITELLM_DATABASE_URL`: the URL supplied by an approved environment
  secret mechanism. Omit `--database-url-file` in this case. Do not type a
  password-bearing assignment into shell history or print the environment.

Literal URL arguments are rejected, including a URL passed to a file option.
Do not put credentials in scripts, terminal recordings, reports or issue text.
The examples contain no stored password. This password-free URL illustrates the
shape only; it is not a working credential or a command-line argument:

```text
postgresql://fleetdiff_reader@db.example.invalid/litellm?sslmode=verify-full
```

The URL must name one user, database and host. If the server requires a password,
the approved secret mechanism supplies it, URL-encoded, in the credential source.
Native export does not recover a missing password from any other file.

Remote connections require `sslmode=verify-full`, verifying both the certificate
chain and the database hostname. Optional `sslrootcert` selects a local CA file;
otherwise system trust roots apply. `verify-ca`, opportunistic TLS and remote
plaintext are rejected. Only a numeric loopback address, such as `127.0.0.1`
or `::1`, or a Unix socket may use explicit `sslmode=disable`; the name `localhost`
does not qualify for the plaintext exception.

The only URL query keys are `sslmode`, optional `sslrootcert`, and `host` for an
absolute Unix socket directory only. A Unix socket URL omits the authority host
and uses `sslmode=disable`. Duplicate or other query keys are rejected. No native
client-certificate, service, passfile, arbitrary connection-option or multi-host
fallback is supported. Use the manual `psql` recipe with administrator-approved
settings for those setups, not a native security override.

Native configuration neutralizes all inherited `PG*` variables and does not load
`~/.pgpass`, `~/.pg_service.conf`, system service files or implicit client
certificates. Installing a driver dependency does not enable these settings.
Diagnostics omit the URL, host, user, password and raw driver/server errors.

## Administrator Setup

Have the database administrator review and run these statements in the intended
LiteLLM database, replacing the example database name `litellm` as needed.
**These are administrator provisioning actions, never SQL executed by fleetdiff.**
The new login owns no database objects and needs no privileged role memberships.

```sql
CREATE ROLE fleetdiff_reader LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE
  NOINHERIT NOREPLICATION NOBYPASSRLS;
GRANT CONNECT ON DATABASE litellm TO fleetdiff_reader;
GRANT USAGE ON SCHEMA public TO fleetdiff_reader;
GRANT SELECT (
  request_id, call_type, api_key, "user", team_id, organization_id, end_user,
  model, model_id, model_group, custom_llm_provider,
  prompt_tokens, completion_tokens, total_tokens, spend, status,
  session_id, agent_id, "startTime", "endTime", metadata
) ON TABLE public."LiteLLM_SpendLogs" TO fleetdiff_reader;
ALTER ROLE fleetdiff_reader SET default_transaction_read_only = on;
```

Only when daily counters are needed, also grant:

```sql
GRANT SELECT (date, api_requests, successful_requests, failed_requests)
  ON TABLE public."LiteLLM_DailyUserSpend" TO fleetdiff_reader;
```

No sequence, write, schema-create or all-table grants are needed. Column-level
SELECT excludes message/response columns, but `metadata` itself is sensitive;
only the fixed projection is exported. The administrator must review existing
`PUBLIC` grants, memberships and row-security policies: these grants do not
remove privileges received elsewhere. Configure authentication through the
normal approved process; no password is set in this SQL. The role default is
defense in depth; every tool transaction independently selects read-only mode.
See PostgreSQL's [GRANT](https://www.postgresql.org/docs/17/sql-grant.html) and
[CREATE ROLE](https://www.postgresql.org/docs/17/sql-createrole.html) documentation.

## Export Without Analysis

Choose a destination that does not yet exist under an existing, trusted parent.
Replace these historical UTC bounds with your own complete interval:

```sh
export_dir="$HOME/litellm-export"
./fleetdiff-install/fleetdiff export litellm \
  --database-url-file "$DATABASE_URL_FILE" --out "$export_dir" \
  --from 2026-10-01T00:00:00Z --to 2026-10-15T00:00:00Z \
  --format csv --daily --routing-details
```

Omit both `--from` and `--to` for the default last 14 complete UTC days. Supply
both to override it; the start is inclusive and the end exclusive. Requests are
selected by `startTime`, interpreted as UTC under the pinned schema.

| Option | Output |
| --- | --- |
| `--format csv` (default) | `spend.csv` |
| `--format jsonl` | `spend.jsonl`, instead of CSV |
| `--daily` | Adds `daily.csv`, always CSV, from the same snapshot |
| `--routing-details` | Adds the three pinned routing columns; default off for standalone export |
| `--usage-details=false` | Omits default-on cache and reasoning detail columns |

Native export does not emit JSON arrays. Existing JSON arrays remain accepted by
file-based readers. `--daily` requires midnight-to-midnight UTC bounds. Review
[routing fields](../examples/litellm-spend/README.md#routing-details-opt-in),
[usage details](../examples/litellm-spend/README.md#usage-details-default-on) and
[daily coverage](ROW_COVERAGE.md) for their accounting meaning.

For later offline analysis of the example above:

```sh
./fleetdiff-install/fleetdiff investigate --litellm-spend "$export_dir/spend.csv" \
  --litellm-daily-spend "$export_dir/daily.csv" \
  --before-period 2026-10-01T00:00:00Z/2026-10-08T00:00:00Z \
  --after-period 2026-10-08T00:00:00Z/2026-10-15T00:00:00Z
```

## Keep An Export Or Run What-If

Both convenience modes create private temporary CSV exports and remove them
after analysis, including handled failures. `--keep-export NEWDIR` retains a
completed export, even if later analysis fails; the destination must be new.
Do not combine `--litellm-db` with local spend, daily-spend or summary inputs.

```sh
./fleetdiff-install/fleetdiff investigate --litellm-db \
  --database-url-file "$DATABASE_URL_FILE" --routing-audit \
  --keep-export "$HOME/litellm-routing-export"
```

Convenience exports include routing details automatically, so routing audits,
route/agent scopes, scenario selectors and workload maps have those fields
without another export option. Missing source metadata stays missing. Existing
scope rules still apply: scoped investigations require explicit before/after
periods and cannot combine scope with daily coverage or session grouping.

For what-if, set `FROM_MODEL` and `TO_MODEL` to reviewed model IDs supported by
your price catalog. The flags `--from` and `--to` **always name models** on this
command; **`--period` selects the UTC date or date interval**, not those flags:

```sh
: "${FROM_MODEL:?Set the source model ID}" "${TO_MODEL:?Set the target model ID}"
./fleetdiff-install/fleetdiff what-if --litellm-db \
  --database-url-file "$DATABASE_URL_FILE" \
  --period 2026-10-01T00:00:00Z/2026-10-15T00:00:00Z \
  --from "$FROM_MODEL" --to "$TO_MODEL" --fraction 0.5
```

Without `--period`, database what-if uses the default export interval. It also
accepts `--keep-export NEWDIR`. Its `--daily` option retains daily counters in the
export; it does not add row-coverage analysis to what-if. `--format text|json`
on either convenience command controls the report, not the private CSV format.
See [what-if accounting](WHAT_IF.md) for coverage, prices and assumptions.

## Snapshot And Ordering

The exporter checks the required table/column shapes and spend request-ID
primary key against the LiteLLM v1.104.0 profile in `public`. Unsupported shapes
fail rather than guessing. All reads use one
`BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY` transaction with UTC settings;
fleetdiff issues no database writes or schema migrations.

The candidate's shared SQL orders spend rows by `"startTime", request_id COLLATE
"C"` and daily rows by `date COLLATE "C"`. For the same database snapshot, bounds,
format and detail options, native and candidate `psql` exports must be
byte-for-byte identical. Sequential exports from a changing live database are
not the same snapshot. This ordering is new relative to the v0.11.0 recipe:
it can change bytes, first-seen aliases and tied report order, not recorded
totals. Database sorting can also affect export time.

## Limits And Failures

| Native export bound | Value |
| --- | --- |
| Spend rows | 10,000,000 |
| Spend file | 16 GiB |
| Encoded row / field | 64 KiB / 8 KiB |
| Statement / idle-in-transaction timeout | 2 minutes each |
| Daily interval / file | 3,660 complete UTC days / 1 MiB |

The existing [spend reader limits](../examples/litellm-spend/README.md#data-and-limits)
also apply before publication. A bound or timeout fails the export, never silently
truncates it. Narrow the interval with your database operator when needed.

The exporter uses a private mode-0700 sibling `.fleetdiff-stage-...`
staging directory under the destination's parent, with mode-0600 files. Staging
is not inside `--out`. The final destination stays absent while all files are
written and validated. Only the complete directory is then published in one
atomic no-replace operation; files are not published individually. Existing
destinations, including empty directories, and symlink components are rejected.
The parent and its ancestors must be owned by you or root, and must not be
writable by others unless protected by the sticky bit (as in a standard `/tmp`).
The filesystem must support an atomic rename that refuses existing destinations;
there is no overwriting fallback.
If another process creates the final destination during export, publication
fails without replacing or removing that destination.

Handled failures before publication remove staging data, not a partial final
directory. A crash before publication may leave only the private sibling
`.fleetdiff-stage-...` directory; it never exposes a partial export at the final path.
After publication, the final directory is complete, even if the process exits
before reporting success. Do not consume staging files; remove only the run's
abandoned staging directory under your retention policy. Publication does not
establish that a subsequent investigation or what-if analysis succeeded.
Exit 0 indicates command success, 1 an export/analysis/output failure, and 2
invalid command options.

Errors deliberately suppress connection details and raw database diagnostics.
Check credential source and permissions, verified TLS, read grants, schema pin,
bounds and a new output path; use approved private server logs for investigation.
Do not publish credentials, raw exports or server diagnostics in bug reports.

## Privacy And Verification

File-based commands stay offline, even with credentials in the environment.
Only explicit native database modes connect; there are no telemetry, update or
provider requests. DNS may resolve the chosen database host. Native exports
retain private keys, users, sessions and routing identities, while excluding
prompts, responses, raw error messages and whole metadata objects. Reports keep
existing aliases and keyed-hash rules; public catalog model IDs remain readable.
Keep exports private and review reports under your organization's sharing policy.
Use `umask 077` when redirecting reports; shell output permissions are separate
from native export permissions.

The candidate pins `pgx/v5 v5.11.0`, `pgpassfile v1.0.0` and
`pgservicefile v0.0.0-20240606120523-5a60cdf6a761`; existing `x/text v0.41.0`
normalization is retained. Dependency names do not imply passfile/service support.
The [candidate verification checklist](../examples/litellm-spend/TESTING.md#native-export-candidate-checks)
lists the database, security and equivalence tests.
