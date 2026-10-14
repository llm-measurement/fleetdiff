# Organization Collector: Rotating Private Logs

**Prototype, not production/account-auth conformance.** This route reuses the
exact redaction in [collector.yaml](collector.yaml); it does not extend the
300-second / 8-MiB try-it helper. Only operator-approved, private collection is
in scope. Observed records remain client-recorded, with finality and completeness
unknown. Default capture intentionally has no model labels or per-model breakdown.

## Compose The Configuration

Use contrib **v0.162.0**, image
`otel/opentelemetry-collector-contrib@sha256:39923a8e431bd1f57be82411999d389fcfe40857492e4365456d97a4c1f74be6`.
The JSON overlay is native collector configuration, not a separate processor:

```sh
umask 077
capture_parent=$(mktemp -d)
export CLAUDE_CAPTURE_DIR="$capture_parent/capture"
mkdir -m 700 "$CLAUDE_CAPTURE_DIR"
otelcol-contrib validate \
  --config=examples/claude-code-logs/collector.yaml \
  --config=examples/claude-code-logs/collector-long-running.json
```

This validates only; it does not start collection. Use an already-installed
verified binary, or the pinned cached container with the directory mounted at
the constant configured path. An approved service invocation uses the same two
`--config` arguments without `validate`. The overlay changes only the receiver
binding and exporter, preserving processors and their order. It binds HTTP/JSON
to `127.0.0.1:4318`, with the inherited 1-MiB receive limit.

For an existing organization collector, incorporate the processor definitions
unchanged in a **dedicated Claude logs pipeline** and use its approved receiver.
Do not blindly merge the example `logs` pipeline over an existing pipeline.
The required order is `memory_limiter`, `filter/events`, `transform/privacy`,
`groupbyattrs/safe_envelope`, then this file exporter. Preserve existing unrelated
pipelines. Do not fan out raw input to a debug or other exporter before redaction.
Validate the fully merged configuration separately. This example does not test
your organization's fanout, remote authentication, TLS, or mixed-tenant receiver.
Loopback is suitable only for same-host senders; do not expose this unauthenticated
receiver to a network. No deployment or service changes are performed here.

## Exact Policy And Limits

The fixed active filename is `logs.jsonl`; only the writer's timestamped backup
names vary. There are no resource-derived paths, custom encoding or compression.

| Setting | Value | Meaning at this pin |
| --- | --- | --- |
| `rotation.max_megabytes` | 8 | 8 * 1024 * 1024-byte size threshold; an oversized single write fails |
| `rotation.max_backups` | 15 | Retains up to 15 backup timestamps after asynchronous cleanup |
| `rotation.max_days` | 15 | Removes backups whose filename timestamps are over 15 * 24 hours old when cleanup runs |
| `rotation.localtime` | false | UTC backup timestamps |
| `append` | false | Required compatibility: `append: true` plus rotation is rejected |
| `create_directory` | false | Operator precreates and owns the private directory |

The [pinned exporter config](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/ae8c507510f48f433ab47dd1c6b01a59d6c388b5/exporter/fileexporter/config.go)
and [writer factory](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/ae8c507510f48f433ab47dd1c6b01a59d6c388b5/exporter/fileexporter/factory.go)
are authoritative for these settings. Rotation uses
[timberjack v1.4.7](https://github.com/DeRuina/timberjack/blob/v1.4.7/timberjack.go)
(`Write`, `openExistingOrNew`, `openNew`, `millRunOnce`), pinned by the exporter's
[go.mod](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/ae8c507510f48f433ab47dd1c6b01a59d6c388b5/exporter/fileexporter/go.mod).
Its rotated filenames include a reason, such as
`logs-2026-10-13T15-49-19.182-size.jsonl`. Existing active files are reopened for
writing by the rotating writer; `append: false` does not mean restart truncation
on this branch. The exporter's normal `flush_interval` is ineffective with this
unbuffered rotation writer.

**Do not interpret `max_days` as timed active-file rotation or strict record TTL.**
The exporter does not configure timberjack's time-rotation options. Cleanup
runs on writer activity/open/rotation, not an independent retention clock;
filename time is not each record's event time. A quiet active file can retain
old records indefinitely. The public README's age-rotation wording is therefore
not a strict TTL contract for this pinned implementation.

With the dedicated naming pattern, the settled nominal file budget is
16 * 8 MiB = 128 MiB (active plus backups), **not a hard directory quota**.
Cleanup is asynchronous and may fail; unrelated files and copied snapshots are
outside that policy. Use an operator-enforced disk quota and alert on exporter
failures, disk usage and cleanup failure. High traffic can evict history before
15 days; no minimum comparison coverage is guaranteed.

## Privacy And Retention Gates

**Important limitation:** `factory.go` supplies file mode `0644`, and timberjack
`openNew` explicitly applies it with `chmod`, bypassing a restrictive umask.
Precreating one `0600` file does not preserve that mode through rotation. The
binary test confirms the next active file and later backups are `0644`.

Require a dedicated, collector-owned `0700` directory and private ancestors,
no broad ACLs/shared mounts, and owner-only access to all copied snapshots.
`0644` inside this directory is an explicitly disclosed exception, not a
`0600` guarantee. If policy requires `0600` on every live file, **do not deploy
this rotating route**. A periodic chmod watcher is not an acceptable substitute.
Keep the tested short helper or await an exporter supporting a verified file-mode
setting. Redaction retains raw attribution IDs; it is not account authentication
or a detector for content disguised as an allowed identity string.

The organization must approve its raw-ID retention period before enabling this
route. If it needs a strict TTL, its existing supervised retention system must
stop/quiesce collection, flush/close the writer, delete expired active/backups
and snapshots, then restart on a fresh private path, including during idle
periods. Do not use external copy-truncate or delete the active file while the
writer is live. This document does not install such a scheduler or certify it.
Absent that external lifecycle, only backup-age/count cleanup is provided.

## Import A Frozen File Set

Stop or quiesce the approved pipeline before freezing files; do not import the
active file during writes or race an asynchronous backup deletion. Record selected
file hashes, client version, capture profile, configuration hash and coverage
gaps privately. Apply owner-only permissions to the frozen review copy and delete
it under the approved retention policy. A snapshot copied elsewhere is not
covered by exporter cleanup.

The prototype importer uses repeated `--claude-code-logs FILE` flags,
not shell concatenation, with aggregate limits across files. For example, on the
updated local checkout:

```sh
GOTOOLCHAIN=local GOPROXY=off go run ./cmd/fleetdiff investigate \
  --claude-code-logs "$snapshot/older.jsonl" \
  --claude-code-logs "$snapshot/newer.jsonl" \
  --group-by user --format json
```

Here `$snapshot` denotes an operator-prepared private immutable snapshot, not a
directory this command creates. Omit periods only to request the labeled default:
two latest complete seven-day UTC periods within the observed span when possible,
otherwise equal observed-span halves. Both explicit period flags override it.
Calendar completeness is not export completeness. The CLI-enabled rotation suite
checks both default selections, repeated copied files and the actual rotated
synthetic files against the local-checkout binary. The importer reads one bounded
byte snapshot for period selection and accounting; its own tests cover aggregate
bounds and snapshot consistency.
See [Importer Inputs And Limits](README.md#importer-inputs-and-limits) before
selecting files. Do not infer semantic
uniqueness from filenames, prompt IDs, or sequence numbers.

## Next Gate

After account-auth and organization privacy/lifecycle conformance, the next
backend candidate is **operator-exported Loki query-range JSON**, not supported
input today. Its source mapping and equal-timestamp pagination need their own
fixtures and version pin. This configuration adds no Loki/Elastic/Datadog exporter,
adapter, credentials or network calls.
