# Security Policy

## Supported Versions

Security fixes target the latest patch in the `0.13.x` release line and `main`.
Use the newest patch and record its version and revision when running trials.
Older patches and development snapshots do not receive routine backports.
Dependencies and the Go toolchain are reviewed on an ongoing basis.

## Reporting A Vulnerability

Please use
[GitHub private vulnerability reporting](https://github.com/llm-measurement/fleetdiff/security/advisories/new).
Do not
disclose a suspected vulnerability, exploit details, secrets, or sensitive
exports in a public issue or discussion.

Include the affected commit, impact, a minimal reproducer, and any known
workaround. Use synthetic data where possible. Reports will be acknowledged as
soon as practical, then assessed and coordinated privately. There is no
contractual response-time commitment.

## Safe Use

- Treat exports and reports as sensitive data. Metadata is cleartext, and keyed
  hashes remain pseudonymous and linkable. Neither is a substitute for access
  control, encryption, data minimization, or differential privacy.
- Transfer exports through approved authenticated channels. Producer IDs, key
  IDs, accounting settings, and disjoint request ownership are declarations, not
  authenticated proof. The current summary format has no signature.
- Use an expected producer list from trusted inventory. Do not remove missing
  producers just to obtain a complete report or interpret partial data as savings.
- Limit access to input files, output files, terminal recordings, and CI logs.
  `--show-hashes` deliberately exposes linkable pseudonyms; `inspect --show-names`
  exposes custom attribute names, which may themselves contain sensitive data. Shell redirection
  uses the caller's permissions; use a restrictive umask for saved reports.
- Build with a current security-patched Go version supported by the README.
  Pin dependencies and collector images, and review dependency updates.
- Apply operating-system resource limits when processing untrusted files. Input
  byte and file limits are not a guarantee of constant CPU time or maximum RSS.
- `inspect` reads raw OTLP captures locally. Keep captures private, use the default
  per-run key and aliases for shared reports, and remove captures after inspection.
  Raw values are transiently decoded in process memory; this is not secure erasure.

File-based commands read local files and write stdout/stderr without network
access; inspection also accepts stdin. Merely setting database credentials does
not enable a connection. Initial builds may download dependencies, and the live
example starts local Docker collectors and may download its pinned image.

## Native Database Export

Only explicit `export litellm`, `investigate --litellm-db` and `what-if --litellm-db`
connect to PostgreSQL. This adds a network boundary, not telemetry, update checks
or provider calls.

- Supply exactly one credential source: `FLEETDIFF_LITELLM_DATABASE_URL` or
  `--database-url-file` pointing to an owned regular mode-0600 file. Literal
  URL arguments and symlink components are rejected. Native export ignores
  inherited `PG*` settings, `~/.pgpass`, PostgreSQL service files and implicit
  client certificates. It never prints connection details or raw driver errors.
- Remote connections require `sslmode=verify-full`; `verify-ca` is not accepted.
  Only numeric loopback addresses or Unix sockets may use `sslmode=disable`.
  The query allowlist is `sslmode`, optional `sslrootcert`, and `host` only for
  an absolute Unix socket directory. Client-certificate and service-based setups
  use the separately reviewed `psql` recipe, not a native TLS bypass.
- Use a dedicated SELECT-only role. Every native export uses one read-only,
  repeatable-read snapshot with bounded statements and idle time. Role creation
  and grants are administrator actions; fleetdiff never changes the database.
- Native export writes private local files. Destinations must be new, with no
  symlink components; existing directories, even empty ones, and overwrites are
  refused. Files are built in a mode-0700 sibling `.fleetdiff-stage-...` directory,
  with mode-0600 files. The final destination stays absent until the complete
  directory is atomically published without replacement, including when another
  process creates the destination during export. Convenience modes remove
  temporary exports unless `--keep-export NEWDIR` is selected.
- Exports retain raw identities. Reports keep existing aliases and keyed-hash
  rules; optional label/hash disclosure remains explicit. A crash before
  publication may leave private sibling `.fleetdiff-stage-...` staging data, never a
  partial final directory. After publication, the final directory is complete.
  Remove only the run's abandoned staging data under your retention policy.

See the [native export contract](docs/NATIVE_EXPORT.md) for setup, exact grants,
limits and failure handling. See the
[comparison contract](docs/COMPARISON.md) for input limits and trust assumptions.
See [inspection](docs/INSPECT.md) for bounded decoding, source semantics, and key handling.
See [operations](docs/OPERATIONS.md) for installation verification, isolated
execution, upgrades, and rollback.
