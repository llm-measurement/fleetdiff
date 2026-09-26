# Input Resource Measurements

Date: 2026-09-24. These are synthetic sizing checks, not workload-independent
latency guarantees, a throughput benchmark, or evidence of business savings.

## Environment

- Apple M4 Max, 16 CPU cores, 64 GiB RAM; macOS 27.0 build 26A428, ARM64.
- Go 1.26.8; native binary built with `-trimpath`, without race instrumentation.
- Baseline `3fd4fc59a268accfe5abd3f7a19e7acb5562a705` plus the uncommitted
  input-validation changes described in the Unreleased changelog.
- Three fresh CLI processes per case; ordinary OS caching, no cache purge,
  warmup, CPU isolation, or hard memory limit. Other machine activity was not
  controlled. The two input windows are read and compared in each process.
- Parent-side fixture construction is excluded. Wall time includes startup,
  file reading, parsing, combining, and JSON output. Peak RSS is the child
  process's `getrusage` value, not allocated bytes or the test runner's RSS.

## Results

| Case | Files per side | Encoded bytes per side | Producers | Time in ms, runs 1/2/3 | Peak RSS in bytes, runs 1/2/3 |
|---|---:|---:|---:|---|---|
| File-count limit, counter-only | 512 | 219,136 | 128 | 225 / 33 / 33 | 12,812,288 / 12,288,000 / 12,288,000 |
| Near byte limit, dense HLL | 47 | 32,951,653 | 47 | 412 / 411 / 399 | 142,196,736 / 144,900,096 / 141,803,520 |
| Near byte limit, full frequent-items | 108 | 33,266,916 | 108 | 4,726 / 4,776 / 4,791 | 130,596,864 / 130,088,960 / 130,416,640 |

Both sketch cases contain the maximum 16 payloads per file. HLL uses precision
15 in dense form after 200,000 deterministic hash updates. Frequent-items uses
1,024 retained entries, each with weight 1,000, in the default profile. These
measurements have unrecognized names on purpose: undisplayed data is still
validated and combined. The counter-only case contains four cumulative snapshots
per producer; later snapshots replace earlier ones. Every run produced a complete
valid report. The largest observed RSS was 138.2 MiB; the longest run was 4.791 s.
Do not treat either as a worst-case ceiling over all valid or malformed inputs.

A separate malformed-state case encoded 200,000 repeated protobuf frequent-item
entries in a 3,467,295-byte JSON file. One fresh process rejected it in 36 ms,
with 34,783,232 bytes peak RSS, exit status 1, and zero report bytes. This probes
decoder allocation before retained-entry validation, not every possible malformed
encoding. Run it with `-run '^TestMalformedResourceUsage$'` using the same binary
environment variable below.

Reproduce from the repository root:

```sh
go build -trimpath -o bin/fleetdiff ./cmd/fleetdiff
FLEETDIFF_RESOURCE_BINARY="$(pwd)/bin/fleetdiff" \
  go test ./internal/compare -run '^TestResourceUsage$' -v -count=1 -timeout=10m
```

Use the same Go patch version for comparisons. The test starts a fresh binary
for each run and imposes a 90-second per-process timeout, but no memory ceiling.
Use an OS/container memory limit for adversarial workloads. Keep production
inputs at or below the documented limits and measure your own mix.

## Restricted Binary Checks

Linux ARM64 and AMD64 archives passed the small committed example with
`--network none`, a read-only root, UID 65532, all capabilities dropped,
`no-new-privileges`, one CPU, 256 MiB memory, and a 64-process limit. Inputs were
mounted read-only. ARM64 ran in Docker Desktop's Linux VM; AMD64 used emulation.
Neither result is native Linux throughput evidence.

The macOS ARM64 archive passed locally. The AMD64 macOS archive built but could
not execute on this host (`Bad CPU type in executable`; no compatible execution
environment). The subsequent [native CI run](https://github.com/llm-measurement/fleetdiff/actions/runs/36046002066)
passed on Intel macOS, ARM64 macOS, and both Linux architectures, including the
restricted Linux runtime checks. Initial sandboxed
cross-compilation also failed on toolchain/cache permissions, then succeeded
with the required filesystem access. These are disclosed non-passing checks,
not fleetdiff input-processing failures.
