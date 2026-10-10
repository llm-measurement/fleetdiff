# Input Resource Measurements

## LiteLLM Spend Import (Source Checkout)

On October 10, 2026, the source-built importer read one million synthetic rows
in **4.90-15.25 seconds**, with peak RSS below **172 MiB**. Both CSV cases met
the predeclared target of 60 seconds and 512 MiB on the M4 Max.

| Format | Keys | Wall seconds | Peak RSS bytes | OS measurement |
| --- | --- | ---: | ---: | --- |
| CSV | Three repeated keys | 4.92 | 178,356,224 | [record](benchmarks/litellm-spend-2026-10-10-csv.txt) |
| CSV | One million unique keys | 4.90 | 178,077,696 | [record](benchmarks/litellm-spend-2026-10-10-unique.txt) |
| JSONL | Three repeated keys | 13.71 | 179,372,032 | [record](benchmarks/litellm-spend-2026-10-10-jsonl.txt) |
| JSON array | 100,000 shared keys | 15.25 | 179,191,808 | [record](benchmarks/litellm-spend-2026-10-10-json.txt) |

Machine: Apple M4 Max, 16 cores, 64 GiB RAM; macOS 27.0.1 (26A434), arm64;
Go 1.26.9, `-trimpath`, without race instrumentation. One fresh CLI process per
case, no CPU isolation or cache purge. Each run includes file reading, bounded
decoding, exact request-ID duplicate tracking, accounting, sketches, and JSON
output. Generation is excluded. All four reports contained 1,000,000 rows and
400,000,000 before / 800,000,000 after recorded tokens.

Reproduce from this source checkout on macOS; choose a new destination:

```sh
go build -trimpath -o bin/fleetdiff ./cmd/fleetdiff
bench_dir="$(mktemp -d "${TMPDIR:-/tmp}/fleetdiff-spend.XXXXXX")"
python3 -B examples/litellm-spend/generate.py --rows 1000000 --format csv \
  --output "$bench_dir/input.csv"
FLEETDIFF_BENCH_SECRET=llm-measurement-p1-public-benchmark-key \
  /usr/bin/time -l bin/fleetdiff investigate --litellm-spend "$bench_dir/input.csv" \
  --before-period 2026-10-07 --after-period 2026-10-08 \
  --hash-secret-env FLEETDIFF_BENCH_SECRET --format json \
  > "$bench_dir/report.json" 2> "$bench_dir/resources.txt"
```

For the other rows, add `--unique-keys` to generation, or use `--format jsonl`,
or `--format json --key-cardinality 100000`; match the input extension in both
commands. The secret above is public test data. See the
[generator and integration checks](../examples/litellm-spend/TESTING.md).
These runs use explicit periods, two models, and synthetic records; automatic
period selection adds a first pass. They measure this machine and workload,
not a worst-case ceiling for every accepted export.

## Scan Engine (Source Checkout)

On 2026-10-04, `BenchmarkScan30Windows` measured 30 already-decoded synthetic
windows with ten user keys per window, a 24-window baseline, and two recent
windows. The three runs took 1.629, 1.881, and 1.843 ms per scan. The slowest
run was 1.881 ms; allocations were 7,105,415 to 7,105,841 bytes per scan and
18,160 to 18,161 allocations. Allocated bytes are not peak resident memory.

Environment: Apple M4 Max, 16 cores, 64 GiB RAM; macOS 27.0.1; Go 1.26.8,
darwin/arm64, without race instrumentation. Fixture generation, file reading,
JSON decoding, CLI startup, and report formatting are excluded. No CPU isolation
or background-activity controls were applied; other local tests were running.
This is an in-memory engine
microbenchmark from the unreleased source checkout, not a release-binary latency
or a bound for larger histories. Reproduce after building this checkout:

```sh
go test ./internal/compare -run '^$' -bench '^BenchmarkScan30Windows$' -benchmem -count=3
```

## Published Comparison Binary

On one Apple M4 Max, synthetic inputs near the size limits compared in 29 ms to
4.708 s, with at most 136.5 MiB peak memory. Commands to reproduce follow below.

Measured on 2026-09-30 from a clean clone of published tag
[`v0.2.0`](https://github.com/llm-measurement/fleetdiff/releases/tag/v0.2.0),
using the [checked-in resource tests](https://github.com/llm-measurement/fleetdiff/blob/84a621f80d423aeb8a379a3d978620746edd3efb/internal/compare/resource_test.go).
The [complete test output](benchmarks/v0.2.0-2026-09-30.txt) accompanies these results.
These results describe v0.2.0; the v0.3.0 contributor features need a separate run.

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

All measured valid-input processes succeeded; the malformed input was rejected
before report output. Results cover a source-built macOS ARM64 binary on the
machine above. They are sizing observations, not worst-case ceilings or predictions
for other workloads and platforms. See [Operations](OPERATIONS.md) for deployment
and release verification.
