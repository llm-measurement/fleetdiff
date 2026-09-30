# Input Resource Measurements

Date: 2026-09-30. These are synthetic sizing checks, not workload-independent
latency guarantees, a throughput benchmark, or evidence of business savings.
They were run from a clean clone of published tag
[`v0.2.0`](https://github.com/llm-measurement/fleetdiff/releases/tag/v0.2.0),
using the [checked-in resource tests](https://github.com/llm-measurement/fleetdiff/blob/84a621f80d423aeb8a379a3d978620746edd3efb/internal/compare/resource_test.go).
The [complete test output](benchmarks/v0.2.0-2026-09-30.txt) accompanies these results.
This measures that release, not the current unreleased contributor features.

## Environment

- Apple M4 Max, 16 CPU cores, 64 GiB RAM; macOS 27.0 build 26A428, ARM64.
- Go 1.26.8; native binary built with `-trimpath`, without race instrumentation.
- Public revision `84a621f80d423aeb8a379a3d978620746edd3efb`, with no source
  changes. Dependencies came from its committed module files. Go build and module
  caches started empty; build time is excluded from the results.
- Three fresh CLI processes per case; ordinary OS caching, no cache purge,
  warmup, CPU isolation, or hard memory limit. Other machine activity was not
  controlled. The two input windows are read and compared in each process.
- Parent-side fixture construction is excluded. Wall time includes startup,
  file reading, parsing, combining, and JSON output. Peak RSS is the child
  process's `getrusage` value, not allocated bytes or the test runner's RSS.

## Results

| Case | Files per side | Encoded bytes per side | Producers | Time in ms, runs 1/2/3 | Peak RSS in bytes, runs 1/2/3 |
|---|---:|---:|---:|---|---|
| File-count limit, counter-only | 512 | 219,136 | 128 | 29 / 29 / 29 | 12,517,376 / 12,419,072 / 12,500,992 |
| Near byte limit, dense HLL | 47 | 32,951,653 | 47 | 450 / 391 / 391 | 143,097,856 / 141,754,368 / 140,886,016 |
| Near byte limit, full frequent-items | 108 | 33,266,916 | 108 | 4,608 / 4,633 / 4,708 | 130,269,184 / 130,236,416 / 130,711,552 |

Both sketch cases contain the maximum 16 payloads per file. HLL uses precision
15 in dense form after 200,000 deterministic hash updates. Frequent-items uses
1,024 retained entries, each with weight 1,000, in the default profile. These
measurements have unrecognized names on purpose: undisplayed data is still
validated and combined. The counter-only case contains four cumulative snapshots
per producer; later snapshots replace earlier ones. Every run produced a complete
valid report. The largest observed RSS was 136.5 MiB; the longest run was 4.708 s.
Do not treat either as a worst-case ceiling over all valid or malformed inputs.

A separate malformed-state case encoded 200,000 repeated protobuf frequent-item
entries in a 3,467,295-byte JSON file. One fresh process rejected it in 144 ms,
with 33,046,528 bytes peak RSS, exit status 1, and zero report bytes. This probes
decoder allocation before retained-entry validation, not every possible malformed
encoding. It ran first, followed by the three valid-input cases in table order.

## Reproduce

These commands build and run both checks from the published source. The revision
printed below should be `84a621f80d423aeb8a379a3d978620746edd3efb`:

```sh
git clone --branch v0.2.0 --single-branch --depth 1 \
  https://github.com/llm-measurement/fleetdiff.git fleetdiff-v0.2.0
cd fleetdiff-v0.2.0
git rev-parse HEAD
export GOWORK=off
export GOTOOLCHAIN=go1.26.8
go version
go build -trimpath -o bin/fleetdiff ./cmd/fleetdiff
FLEETDIFF_RESOURCE_BINARY="$(pwd)/bin/fleetdiff" \
  go test ./internal/compare \
  -run '^(TestResourceUsage|TestMalformedResourceUsage)$' -v -count=1 -timeout=10m
```

Use the same Go patch version for comparisons, and record your machine and OS.
The test starts a fresh binary
for each run and imposes a 90-second per-process timeout, but no memory ceiling.
Use an OS/container memory limit for adversarial workloads. Keep production
inputs at or below the documented limits and measure your own mix.

## Scope

All measured valid-input processes succeeded. The malformed-input process failed
as expected without emitting a report. These are local macOS ARM64 measurements
of a source-built binary, not downloaded release archives or Linux/container
performance. This rerun did not exercise other operating systems, architectures,
or hosted CI. See [Operations](OPERATIONS.md) for deployment restrictions and
the separate release-verification procedure.
