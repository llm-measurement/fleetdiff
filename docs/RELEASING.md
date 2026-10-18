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

The native archive check also runs `TestReleaseBinaryTopK` against the unpacked
executable. `TestReleaseBinaryInspect` checks stdin inspection, token accounting,
label filtering, and opt-in name display. Together they check user/session attribution,
the strict share threshold, missing usage, absent older measurements, and hidden
private metadata. To repeat
that check locally after verifying and extracting an archive. The checkout also
checks diagnosis exit codes and scan's quiet, unusual, and unavailable states
from the packaged binary:

```sh
FLEETDIFF_RELEASE_BINARY=/path/to/fleetdiff go test ./internal/cli \
  -run '^TestReleaseBinary' -count=1 -v
```

`TestReleaseBinarySpend` checks the LiteLLM synthetic example in CSV, JSON and
JSONL on each platform, including raw-identity suppression and quality counts.
The Linux AMD64 archive also runs the pinned LiteLLM writer -> PostgreSQL ->
read-only SQL -> packaged importer checks. This path uses synthetic data, no
provider keys and no paid calls.

`TestReleaseBinarySessionBudget` checks the cap-only command, observed-span
fallback, explicit periods and clipping in each native archive. The existing
spend examples also exercise the pinned rejection classification and privacy
sentinels with the packaged importer.

`TestReleaseBinaryWhatIf` checks the simple and scenario commands against the
checked text/JSON output, plus workload mappings, summary gaps and error privacy.
Before packaging v0.11.0, reproduce its public numeric bundle offline:

```sh
python3 -B scripts/reproduce-model-mix.py --check
python3 -B -m unittest discover -s evidence/model-mix -p 'test_*.py'
```

For a release, use a clean reviewed commit, update the changelog, and create a
signed version tag. The build script verifies the requested tag points at the
checked-out commit and rejects dirty release builds. Protect release tags and
review changes to workflow files as code with release authority.

Release preparation is separate from release authorization. Merge only after
the maintainer approves the prepared changes; creating or pushing a tag and
publishing require separate explicit approval. Use signed, signed-off preparation
commits. Do not create a local release tag just to test packaging; use the
development package path above.

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

The workflow uses the version's notes in `docs/releases/` as its draft release
body. Prepare the installer default and matching README instructions on the
release PR, keeping them held for review until release approval. Before outreach,
verify that every advertised version is downloadable. Do not replace immutable tags
or artifacts to correct a failed publication; fix forward with a new version if
any release content must change.

## After Publishing v0.11.0

From an anonymous clone in a fresh directory outside any synced workspace:

1. Download all four platform archives, checksums, SBOM and provenance. Verify
   each archive and manifest with the workflow and tag identity constraints in
   [OPERATIONS](OPERATIONS.md#install-and-verify). Confirm the signed tag and
   recorded build revision match the approved commit.
2. Run the README's verified installer into a new directory. Confirm the native
   executable reports `v0.11.0`, then run the packaged-binary tests above.
3. Set `FLEETDIFF_BIN` to that freshly installed executable and run the exact
   [pinned database test](../examples/litellm-spend/TESTING.md#pinned-database-integration).
   It exercises the real pinned writer, a SELECT-only SQL export and the released
   importer. Check that its database tests ran rather than skipped.
4. Run the README's synthetic example with that executable and compare its
   selected output. Keep command logs, asset hashes, signature/attestation
   results, platform/version and test output in private `docs/local/` evidence.
5. Run the session-budget sample without `--period` or `--group-by`: check the
   3-of-4 headline, exact USD percentiles, observed-span label and two above-cap
   sessions with no recorded session-budget rejection. Repeat with
   `--period 2026-10-03`: no session should be labeled clipped or possibly clipped.
6. Run the cache-drop fixture and inspect the shipped synthetic capture with
   Go absent from PATH. Check the cache headline, coverage, keyed rankings and
   unchanged capture bytes.
7. Run the [row-coverage fixture](ROW_COVERAGE.md#reading-coverage): require
   8 exported rows versus 9 daily requests and one missing row on October 7,
   with matched counts on October 8. Repeat in JSON and without the optional
   daily input; the latter must match the v0.8.0 golden output.
8. Run the paired SQL helper against the pinned test DB and repeat coverage with
   that export. Record failure-logging-disabled behavior: a request absent from
   both tables cannot be found by their count comparison.
9. Run the [what-if sample](WHAT_IF.md#no-data-handy-try-the-sample) with the
   installed executable: require USD 0.154400 -> 0.109398 to 0.147974 under
   workload assumptions, level 1 USD 0.148703, three covered observations and
   one missing-split exclusion. Repeat in JSON and with the workload map;
   require one mapped result and no raw fixture identities. Reproduce the
   public factors from the tagged checkout with the commands above. Site bumps
   and outreach stay separate until these released-artifact checks pass.
10. Exercise failure rows with recorded tokens and sessions with absent or
    partial agent attribution. Verify the failure subtotal does not alter
    recorded or re-priced totals, pricing exclusions apply to the failure
    subset, and absent agents read as unknown. Keep the v0.8.0 import-refactor
    output goldens and legacy identity-hash tests unchanged; reports without
    these conditions must remain byte-identical.
11. Run the routing guide with default aliases and with `--show-labels`. Verify
    public model names, explicit configuration-label warnings, missing serving
    identity, classifier overhead and raw-identity suppression. Exercise a
    pinned-writer `openai/<public-id>` export: price it only when the row provider
    and catalog provider both equal the prefix. Repeat with mismatched and
    missing providers; those rows remain unknown. The packaged
    `TestReleaseBinaryStructuredModelPricing` covers scoped and unscoped pricing.

The release workflow tests every native platform before publication. The local
outsider run proves the post-publication download and database path on its own
platform; record that scope separately.

The workflow uses GitHub OIDC and a short-lived signing identity. Repository and
workflow permissions control who can publish; review those alongside provenance.

Before accepting external security reports, enable GitHub private vulnerability
reporting and test the link in SECURITY.md. Confirm the contact and supported
version policy.
