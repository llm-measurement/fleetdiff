# Security Policy

## Supported Versions

Security fixes target the latest patch in the `0.4.x` release line and `main`.
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

The comparison and inspection commands read local files and write stdout/stderr
without network access. Inspection also accepts stdin. Initial builds may download dependencies, and the live example starts
local Docker collectors and may download its pinned image. See the
[comparison contract](docs/COMPARISON.md) for input limits and trust assumptions.
See [inspection](docs/INSPECT.md) for bounded decoding, source semantics, and key handling.
See [operations](docs/OPERATIONS.md) for installation verification, isolated
execution, upgrades, and rollback.
