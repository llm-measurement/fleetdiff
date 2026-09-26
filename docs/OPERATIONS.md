# Operating fleetdiff

fleetdiff is a local, read-only comparison command, not a service. It needs no
administrator privileges, inbound port, database, model credentials, or hashing
secret. It does not upload data, check for updates, or send usage telemetry.

## Install And Verify

The initial supported targets are Linux and macOS on AMD64 and ARM64. Native
Windows binaries are not provided. Download a binary from the
[release page](https://github.com/llm-measurement/fleetdiff/releases/tag/v0.1.1).
Go is not needed to run it. The commands below use `curl` for downloading and
the [GitHub CLI](https://cli.github.com/) for provenance verification.

Choose `linux_amd64`, `linux_arm64`, `darwin_amd64` (Intel Mac), or `darwin_arm64`
(Apple Silicon). Run in a new, empty directory:

```sh
version=v0.1.1
target=linux_amd64
archive="fleetdiff_${version}_${target}.tar.gz"
base="https://github.com/llm-measurement/fleetdiff/releases/download/$version"
curl --fail --location --remote-name "$base/$archive"
curl --fail --location --remote-name "$base/provenance.jsonl"
gh attestation verify "$archive" --bundle provenance.jsonl \
  --repo llm-measurement/fleetdiff \
  --signer-workflow llm-measurement/fleetdiff/.github/workflows/release.yml \
  --source-ref "refs/tags/$version" --deny-self-hosted-runners
```

Only after verification succeeds, extract the archive and check the binary:

```sh
tar -xzf "$archive"
./fleetdiff --version
```

Release archives contain `fleetdiff`, its license, and runtime dependency licenses.
They are accompanied by `SHA256SUMS`, an SPDX dependency inventory, build metadata,
and a signed provenance bundle. Checksums alone do not authenticate a download.
The verification command checks both the archive digest and its signing identity.
`SHA256SUMS` is
also attested: verify it with the same command before using it to check a full
downloaded asset set with `shasum -a 256 -c SHA256SUMS`.
For a restricted network, have your artifact administrator verify the download
on a connected machine and copy it through the approved internal mirror.
To verify offline, transfer the provenance bundle and an independently approved
Sigstore trust root too; use `gh attestation verify --bundle provenance.jsonl
--custom-trusted-root trusted_root.jsonl` with the same identity constraints.
See GitHub's [offline verification instructions](https://docs.github.com/en/actions/security-for-github-actions/using-artifact-attestations/verifying-attestations-offline).
Do not trust a replacement root merely because it arrived with the archive.

macOS binaries are not Apple Developer ID signed or notarized. Organizations
requiring that must approve or sign the binary through their normal software
distribution process. Do not disable Gatekeeper or endpoint protection to run it.

### Build From Source

Build with a currently patched Go 1.25 or 1.26 toolchain:

```sh
go mod verify
go build -mod=readonly -trimpath -o bin/fleetdiff ./cmd/fleetdiff
bin/fleetdiff --version
```

Unstamped checkout builds report `dev`. Tagged `go install` builds report the
embedded module version. Release archives include a stamped version and revision;
the revision remains `unknown` in unstamped builds.

Builds can download Go dependencies and toolchains. Runtime comparison cannot.
For an offline build, prepare the approved toolchain and module cache in a
connected build environment; use `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`
only after verifying those cached dependencies there. Do not disable checksum
verification for ordinary connected builds. An offline user normally needs only
the reviewed binary, not Go, Git, Docker, or a compiler.

## Run On Real Exports

1. Use a dedicated, unprivileged account. Grant it read access only to approved
   completed exports. Do not grant credentials to the model or collector.
2. Freeze a copy of both windows in directories that other users cannot rewrite
   during comparison. Symlinks and special files are rejected; parent-directory
   ownership and a trustworthy filesystem remain the operator's responsibility.
3. Obtain expected producer IDs from trusted inventory. Agree on scope,
   measurement settings, compatible keys, and disjoint event ownership.
4. Restrict input and report permissions. Use `umask 077` before redirecting
   reports; stdout otherwise follows the caller's permissions and logging setup.

Producer IDs and matching key IDs are not authentication. The comparison detects
structural incompatibility, not a dishonest producer inventing valid counters or
hashes. Transfer exports through an authenticated channel such as your approved
object store or SSH workflow. Where required, verify a sender-signed manifest
binding file digests to producer, scope, window, epoch, and sequence before use.
fleetdiff does not implement that signature protocol. A checksum detects changed
bytes only if the expected checksum itself is trusted.

The command has no raw prompt or identity recovery. Summary metadata is cleartext;
hashes are pseudonymous and can be linked across exports with compatible keys.
Even default reports reveal timing, volumes, and concentration patterns. Apply
retention and sharing policy to both exports and reports. `--show-hashes` is an
explicit additional disclosure, not a harmless display preference.

## Limits And Sizing

Each side permits at most 512 JSON files, 1,024 directory entries, and 32 MiB of
encoded input. One summary is limited to 8 MiB. Expected producers are limited
to 128; summaries permit at most 16 sketch payloads and 128 counters. These are
validation limits, not a fixed memory reservation or an execution-time promise.
Unselected windows still count against input limits and must parse correctly.

Use OS/container memory, CPU, process, and wall-time limits for supplier-controlled
exports. Treat timeout or memory termination as a failed comparison, never as a
zero or partial result. Go's `GOMEMLIMIT` is a soft runtime target, not a security
boundary. See [measured input cases](BENCHMARKS.md); size from your real fixtures
and retain headroom. Start with one comparison process, not unlimited parallel
jobs. The demonstration's 256 MiB container test covers its small sample only.

CI checks the packaged Linux binary with no network, non-root UID, no Linux
capabilities, a read-only root, and read-only input mounts. This is a tested
deployment pattern, not a claim of certification against every enterprise image
or endpoint policy.

## Automation

Use JSON, not the human-readable report. See [report versioning](COMPARISON.md#json-compatibility).
Keep `--allow-partial` off for an automated complete-window comparison. Success
does not prove complete upstream instrumentation, billing accuracy, or causation.
Use [compare-to-file.sh](../examples/compare-to-file.sh) to write a validated JSON
report with restrictive permissions and an atomic rename:

```sh
sh examples/compare-to-file.sh ./bin/fleetdiff before after team,partner reports/run.json
```

The output directory must exist and be owned by the caller. The script requires
`jq`; the fleetdiff binary itself does not. Supply an application timeout from
your scheduler. Exit 0 means a report was written; 1 means an input, comparison,
or write failure; 2 means invalid CLI options. Help and version also exit 0.
Report generation validates everything before writing, but a failed stdout write
can leave a prefix of the report. Never consume a file from a failed run.

## Upgrading And Rollback

Record the binary's version, revision, toolchain, and archive digest. First run a
candidate version on frozen synthetic and representative approved exports; compare
JSON contracts, expected totals, coverage, and uncertainty. Review the changelog.
Keep the previous reviewed binary for rollback and compare the same exports.

fleetdiff has no daemon, checkpoint, or mutable application state: restarting it
re-reads inputs and recomputes the report. Collector restarts are different:
nonoverlapping producer epochs are combined, overlapping epochs are rejected,
and incomplete observation intervals remain partial. A restart never fills in
missing usage. Key rotations or changed measurement contracts can require new,
separate comparisons rather than a forced merge.

Security maintenance covers the current `0.1.x` release line, not a production SLA.
See [SECURITY.md](../SECURITY.md) for supported versions and confidential reporting.
