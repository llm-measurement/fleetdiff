# Releasing fleetdiff

## Build Checks

Run tests, vet, vulnerability checks, the live collector example, and the
resource checks appropriate to the change. Review the changelog and supported
platforms. A development package can be built locally without creating a tag:

```sh
sh scripts/build-release.sh dev
```

The output directory must not already exist. Partial output from a failed build
must not be distributed. Development binaries say `dev` and identify dirty
checkouts; these are not release candidates with authenticated provenance.
Archive timestamps are not normalized, so archive bytes are not promised to be
reproducible across separate builds.

## Release Workflow

The release workflow builds four archives with runtime dependency licenses,
inventories the unpacked binaries
with Syft, and checks the inventory contains the runtime dependencies. Each
archive is then checksum-verified and exercised on its native platform. Linux
also runs the offline/non-root smoke test. Dependency inventory alone is not a
vulnerability verdict; the source reachability scan is a separate check.

For a release, use a clean reviewed commit, update the changelog, and create a
signed version tag. The build script verifies the requested tag points at the
checked-out commit and rejects dirty release builds. Protect release tags and
review changes to workflow files as code with release authority.

Only a tag push in a **public repository** can run the signing/publishing job.
Private and pull-request builds do not contact public transparency services or
publish releases. This guard must not be removed merely to make a private test
look like a public signed release. GitHub's private-repository attestation
support depends on the organization's plan; use an approved private signing
service for confidential distribution when required.

The public job signs provenance for the archives, SBOM, checksums, and build
metadata. It verifies the archive identity against this repository, workflow,
and tag, then creates a **draft** release. A maintainer must review its assets
and independently follow the verification instructions in OPERATIONS before
publishing. The workflow does not overwrite an existing release.

No signing keys are stored in the repository. The workflow uses GitHub OIDC and
short-lived signing identity. This does not prove the program has no defects or
that a compromised authorized maintainer could not release malicious code.

Before accepting external security reports, enable GitHub private vulnerability
reporting and test the link in SECURITY.md. Confirm the contact and supported
version policy. A release is not an enterprise certification or support SLA.
