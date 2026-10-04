# Local Summary Archive Example

`fleetdiff` remains read-only. This separate, opt-in Go example writes only to the
archive directory explicitly supplied to it. It makes no network requests and
prints nothing on success. Failures use fixed diagnostics, never producer/epoch
identifiers, summary metadata, paths, filenames, or raw operating-system errors.

## Build and Run

From the fleetdiff checkout, explicitly build the helper with a local Go 1.25+
toolchain and dependencies already present in the local module cache:

```sh
umask 077
GOWORK=off GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off \
  go build -mod=readonly -o './bin/fleetdiff-archive' './examples/archive'

sh './examples/archive.sh' \
  '/srv/private summaries/producer-a' \
  '/srv/private history/producer-a' \
  --keep-windows 64
```

Build output and Go caches are an explicit setup step. The wrapper never builds,
downloads dependencies, creates caches, or writes temporary files. It defaults to
`bin/fleetdiff-archive` in this checkout; set `FLEETDIFF_ARCHIVE_HELPER` to an
explicit prebuilt binary path to use another location. Relative source/archive
paths are resolved against the caller's working directory, not the checkout.
Options follow the two paths. Use `--help` for usage. The wrapper suppresses even
shell/loader error paths and reports a generic failure; running the binary
directly provides categorized, still-redacted diagnostics.

SOURCE is one canonical summary file or a nonrecursive producer export directory.
The helper reuses `compare.ReadSeries` and its bounded canonical parser. A single
producer directory is the usual setup. The archive must be a separate, non-nested
location: either a new final directory with existing safe parents, or an existing
directory owned by the invoking user with mode `0700`. Parent directories are
never created or chmodded. Files created by the helper have mode `0600`.

Use real paths without symlink components or `..`. Ancestors must be owned by the
invoking user or root and not group/other writable, except root-owned sticky
temporary directories. On macOS, use `/private/tmp` instead of the `/tmp` symlink.
The helper rejects symlinks, special files, directories inside the archive,
nonprivate owned snapshots, and hard-linked owned snapshots. Confinement uses
`os.Root`; file reads use the existing `localfile.Read` no-follow/nonblocking
checks. POSIX permissions are not an audit of ACLs or a defense against a hostile
same-user process. Use a trusted local filesystem with rename, directory fsync,
and advisory locking support (tested on macOS; not a Windows example).

## Snapshot Semantics

- A window is eligible only when its end and the snapshot's emission time are no
  later than the invocation's wall-clock time. Current/open and future windows,
  including future-emitted snapshots of closed windows, are not archived.
- Every source file is still parsed and validated, including excluded windows.
  The entire source/archive history must share a compatible measurement contract.
  Changing scope, key, accounting contract, or window duration needs a separate
  archive rather than mixing incomparable history.
- For each producer/epoch/window, preserve the newest sequence. The filename is
  `fleetdiff-archive-v1-<20-digit-start>-<sha256(producer-NUL-epoch)>.json`.
  Identifiers cannot contain NUL. No raw identity appears in the filename.
- Collector files are cumulative and overwritten. A higher sequence refreshes
  the archived file by atomic rename, even if its earlier observation already
  covered the complete window. **Do not use `cp -n`**: it freezes the first copy.
- Equal-sequence identical canonical bytes are a no-op. Equal-sequence differing
  bytes fail. Any eligible source snapshot older than its archived sequence fails
  the batch, even if another source file has a newer sequence. Investigate stale
  input or sequence reset; do not silently overwrite newer history.
- `summary.Combine` checks sequence conflicts, counter/observation regressions,
  measurement compatibility, overflow, and overlapping epochs before writes.
  Legitimate non-overlapping restart epochs are retained separately. Partial
  observation intervals and gaps remain exactly as exported; the helper never
  edits coverage metadata or declares an archive/window complete.

The archive still contains sensitive summary metadata and stable hashes. Hashing
filenames is not anonymization or authentication. Preserve the private directory
boundary. Scan must independently receive the operator's expected producer list
and decide whether observation coverage is sufficient; observed producers alone
are not evidence of a complete fleet.

## Retention and Failure Safety

`--keep-windows` defaults to **64** and accepts **1 through 384**. It keeps the
newest distinct observed window starts, including every retained producer/epoch
for those windows. This is not a duration guarantee: gaps do not become zero
traffic, and missing windows are never fabricated.

Both source and archive are bounded by **1024 directory entries**, **512 summary
files**, and **32 MiB total bytes**; each summary is at most 8 MiB. These are the
scan input limits, not the collector's potentially larger export budget. Multiple
producers and restarts consume multiple files per window. Even 384 windows can
exceed the file or byte budget, and 64 large windows may not fit. Archive byte and
entry budgets also count unrelated regular files and interrupted staging files.

Before replacements or pruning, the helper validates the full batch, plans all
retained outputs, and checks budgets. It requires additional staging headroom:
existing bytes plus **all** staged replacements must fit in 32 MiB, and existing
entries plus all staged files must fit in 1024 entries. Existing summary files
plus newly named summaries must fit in 512 files before pruning. It will not
delete useful history first just to create space. Choose smaller retention while
there is headroom, or start a separate private archive; do not expect retention
to repair an already over-budget directory automatically.

A nonblocking directory lock serializes cooperating helper invocations. The whole
batch is staged as exclusive private files inside the archive and fsynced before
any destination is replaced. Existing snapshots and staging files are rechecked.
Only after every planned refresh is installed and the directory is synced may
retention delete old snapshots. Deletion requires a helper-recognized filename,
canonical content whose identity reproduces that name, and unchanged file identity
and bytes. Unrelated regular files are never deleted or overwritten. Unrelated
`.json` files cause a safe failure because scan would try to parse them.

Atomicity is **per file**, not a multi-file transaction. Validation, budget, or
staging failures leave old history intact. A failure during installation can
leave some newer snapshots installed, but retention has not started; rerunning
converges. A failure during pruning can leave extra old files. A process crash
may leave private staging files, which are not scan inputs and are never
automatically reclaimed. Inspect those locally with no helper running before
operator-directed cleanup. Do not modify the archive concurrently with the helper;
schedule scan after a successful archive run for a stable batch.

## Cron Example

After building and testing manually, an operator can install a cron entry such as
the following, adjusting the quoted absolute paths. This example does not install
cron or start a background process itself.

```cron
SHELL=/bin/sh
PATH=/usr/bin:/bin
* * * * * FLEETDIFF_ARCHIVE_HELPER='/srv/fleetdiff/bin/fleetdiff-archive' /bin/sh '/srv/fleetdiff/examples/archive.sh' '/srv/private summaries/producer-a' '/srv/private history/producer-a' --keep-windows 64
```

The one-minute schedule intentionally revisits retained source windows. At a
window boundary, an invocation may still see the last partial export; the next
invocations refresh it after the collector publishes higher sequences. The
helper never archives the current window merely because cron fired. Run more
often than the collector removes closed-window exports, and keep clocks correct.
Stopped/restarted collectors can leave partial epochs; if an epoch disappears
from the source before any closed-window archive run, this helper cannot recover
it. Do not claim completeness from successful execution alone.

Use literal quoted paths. Cron treats `%` specially even inside shell quotes;
avoid it in these paths or escape it according to the local cron implementation.
Keep the exit status visible to your scheduler. On persistent failure, stop and
resolve the source, permission, contract, or capacity issue before running scan.
For useful scan history, retain at least the requested preceding baseline plus
the recent/persistence windows, within the 512-file/32-MiB limits. After archiving,
run the read-only scan with the intended expected producer list and treat partial
coverage or missing baseline history as unavailable evidence, not zero activity.

## Local Tests

For an end-to-end packaged-command smoke check, run this from the checkout with
an unused scratch directory. Setup explicitly builds two binaries and generates
synthetic source files there. The helper itself writes only `history`. The scan
fixture is dated 2026-10-04; run this smoke check after 00:30 UTC on that date.

```sh
set -eu
work='/private/tmp/fleetdiff-archive-smoke'
umask 077
mkdir "$work"
export GOWORK=off GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off
go build -mod=readonly -o "$work/helper" './examples/archive'
go build -mod=readonly -o "$work/fleetdiff" './cmd/fleetdiff'
go run -mod=readonly './examples/scan' --out "$work/source" --quiet
FLEETDIFF_ARCHIVE_HELPER="$work/helper" sh './examples/archive.sh' \
  "$work/source" "$work/history" --keep-windows 8
FLEETDIFF_ARCHIVE_HELPER="$work/helper" sh './examples/archive.sh' \
  "$work/source" "$work/history" --keep-windows 8
"$work/fleetdiff" scan "$work/history" --expected app --baseline 6 \
  --as-of 2026-10-04T00:30:00Z
```

Both helper runs should be silent with exit 0, leaving eight `0600` snapshots in
the `0700` history directory. The final scan should exit 0 and report no unusual
windows, with six of six requested baseline windows. This is a synthetic smoke
check, not evidence that real producer observation intervals are complete. Remove
the chosen scratch directory after inspection. For packaged helper testing against
your own source, only the two `FLEETDIFF_ARCHIVE_HELPER=... sh ...` lines are needed.

The focused test suite additionally exercises snapshot refresh and failure paths:

```sh
GOWORK=off GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off \
  go test -mod=readonly ./examples/archive
GOWORK=off GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off \
  go test -mod=readonly -race ./examples/archive
```

Tests cover cumulative refresh after completion, atomic reader behavior,
idempotence, partial restart epochs, closed-window selection, conflicting and
regressing batches, canonical parsing, retention, private permissions, symlinks,
special files, unsafe parents, hard links, unrelated files, input/output/staging
bounds, simulated out-of-space staging failure, locking, revalidation, and silent
redacted command/wrapper behavior. They use temporary local files, not a live
collector, network access, or a real disk-full/crash experiment.
