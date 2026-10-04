# Can My Traces Answer These Questions?

Start with one application's traces. `fleetdiff inspect` checks model-attempt
accounting, token coverage and origin, and the fields available for user, prompt,
and session questions. It reads locally; it does not start a receiver or contact
your provider.

## Try It With Go Only

From the repository root:

```sh
sh examples/inspect.sh
sh examples/inspect.sh --provenance
```

The script builds the current CLI in a temporary directory, generates six
synthetic spans, prints the inspection, and removes its temporary files. No
collector, Python environment, credentials, or model calls are needed. It also
works when invoked by absolute path from another directory.

Both variants contain four model attempts, a tool span, and an agent wrapper.
The model spans carry 140 input and 30 output tokens. One attempt has no token
fields; another has a normalized `0/0`. In the first variant that zero's origin
is unknown. The second declares it unavailable, so two attempts have missing
usage rather than one. Cache-read 25 and reasoning 5 are subsets, not extra
tokens. Only two model spans carry session IDs.

These are constructed examples of the accounting boundary, not captured LiteLLM
traffic. The separate Docker check below captures actual LiteLLM output.

## Before Capturing Your Data

**A capture is raw telemetry, not a sketch.** It can contain identifiers, prompts,
tool arguments, credentials in attributes, and infrastructure details. Keep it
outside source control, use a private directory, and delete it after inspection.
Share the default inspection report, not the input file. Leave `--show-names`
off when sharing; custom attribute names can themselves contain sensitive data.
Use it locally to resolve an `attribute-N` alias. Leave `--show-hashes`
off when sharing; hashes can link identities across reports when a secret is
reused. Do not paste credentials into commands, reports, or issues.

Use a short capture from traffic you are authorized to inspect. Preserve your
existing backend and sampling policy. The counts describe retained spans, not
all traffic from a sampled or truncated capture. Estimated distinct values
describe one label dimension; total metric series also depend on other labels
and metrics. Low cardinality does not make an attribute safe to expose.

## Route 1: A Collector File Exporter

### A Short Local Capture

This route uses the official **Collector Contrib 0.161.0** image, pinned by
multi-platform digest. The sketches distribution does **not** include `file`.
Install and start Docker, then use two terminals.

Terminal one, from this checkout:

```sh
export CAPTURE_DIR="$HOME/fleetdiff-capture-$(date +%Y%m%d-%H%M%S)"
sh examples/inspect/capture.sh "$CAPTURE_DIR" 30
```

Terminal two: temporarily send your application or a shadow OTLP exporter to
`127.0.0.1:14317` for gRPC, or `http://127.0.0.1:14318/v1/traces` for HTTP. For
an application using an OTLP/HTTP SDK exporter:

```sh
export OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://127.0.0.1:14318/v1/traces
# Run your application briefly, with content capture disabled.
```

The script creates a new 0700 directory, runs as your host UID, publishes only
loopback ports, and stops/removes the container after 30 seconds. Ctrl-C also
stops it. Inspect only after it stops so the exporter has flushed:

```sh
go build -o bin/fleetdiff ./cmd/fleetdiff
bin/fleetdiff inspect "$CAPTURE_DIR"
```

The exporter keeps at most one rotated backup, at 4 MiB per segment; at high
volume this is a rolling sample. A single oversized batch can be rejected.
Restore the application's previous endpoint afterwards, then delete the capture
directory when done. Do not run capture helpers indefinitely or expose these
unauthenticated example receivers beyond loopback. The helper's 30-second timer
is separate from the offline CLI; `fleetdiff inspect` itself never listens.

### Keep An Existing Collector Exporter

If your existing Collector includes `file`, add a second exporter to the **same**
traces pipeline. Retain its current exporters, processors, credentials, and
receiver bindings. The extra entries look like this; `otlp/existing` represents
the exporter already in your configuration:

```yaml
exporters:
  # Keep the full existing otlp/existing configuration here.
  file/inspect:
    path: ${env:FLEETDIFF_CAPTURE_DIR}/traces.jsonl
    format: json
    rotation:
      max_megabytes: 4
      max_backups: 1
service:
  pipelines:
    traces:
      # Keep the existing receivers and processors here.
      exporters: [otlp/existing, file/inspect]
```

Create a 0700 capture directory writable by the collector's service account.
Apply this only for the agreed capture interval, then remove `file/inspect` from
the pipeline and configuration. A file exporter copies pipeline data; it does
not sanitize it. Changes after your sampling/filter processors reflect their
existing choices.

## Route 2: A Python SDK File Capture

The standard console exporter emits diagnostic span JSON, **not OTLP**. This
small exporter uses the public `encode_spans` API and writes real OTLP. It does
not install auto-instrumentation or make network requests.

From this checkout, with Python 3.10 or later:

```sh
umask 077
work=$(mktemp -d)
python3 -m venv "$work/venv"
"$work/venv/bin/python" -m pip install -r examples/inspect/requirements.txt
mkdir "$work/capture"
"$work/venv/bin/python" examples/inspect/sdk_capture.py "$work/capture/sdk.otlp"
go build -o bin/fleetdiff ./cmd/fleetdiff
bin/fleetdiff inspect --input-format file-proto "$work/capture/sdk.otlp"
```

The runnable example emits the same six synthetic spans using real SDK span
objects. To inspect JSON lines instead, choose a **new** output file:

```sh
"$work/venv/bin/python" examples/inspect/sdk_capture.py \
  --encoding json --variant provenance "$work/capture/sdk.jsonl"
bin/fleetdiff inspect "$work/capture/sdk.jsonl"
```

For your application, attach `LocalOTLPExporter` from `sdk_capture.py` to the
existing tracer provider via a `SimpleSpanProcessor`, alongside its current
processors. The example exporter refuses existing files and symlinks, requires
a private parent directory, and caps capture at 1,000 spans or 4 MiB. Exceeding
the limit returns export failure; it does not silently claim complete capture.
Stop the short-lived process and call the provider's `shutdown()` before
inspection. Remove the temporary processor before your next normal run.

After reviewing the report, delete the temporary `work` directory, including its
raw captures. Do not inspect the whole directory containing the virtualenv.

## Route 3: LiteLLM To The Capture Collector

Use the same 30-second capture from Route 1. This recipe is tested with
**LiteLLM 1.102.1**, non-streaming OpenAI-compatible chat. In your existing proxy
configuration, keep the models and other settings, and configure one tracing
callback:

```yaml
litellm_settings:
  callbacks: [otel]
  turn_off_message_logging: true
```

```sh
export OTEL_SEMCONV_STABILITY_OPT_IN=gen_ai_latest_experimental
export OTEL_EXPORTER=otlp_http
export OTEL_ENDPOINT=http://127.0.0.1:14318
export OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=NO_CONTENT
```

Restart your existing proxy with its usual configuration and observe a short,
authorized interval. The endpoint above assumes the proxy runs on the host. A
containerized proxy needs a reachable collector service on a private Docker
network, not its own loopback. Preserve any separate production trace destination
by forwarding to capture from its collector rather than replacing it.

Stock LiteLLM may fill absent usage with zeros or estimates. Without declared
origin, inspect reports **unknown**; it cannot decide whether a particular zero
was sent by the provider.

This pinned proxy emits `gen_ai.request.model` without an operation name, so the
default model fallback counts its attempts. It emits the user as `llm.user`, not
the collector's default `enduser.id` or `user.id`. User readiness therefore stays
unavailable until you map that field or supply a supported user attribute. The
synthetic Go example deliberately supplies `enduser.id`; it is not evidence that
stock LiteLLM provides the same mapping.

For the tested raw-response provenance callback, place this example's `litellm`
directory on `PYTHONPATH` (or mount it read-only at `/callbacks` and set
`PYTHONPATH=/callbacks`). Replace `otel`, do not register both:

```yaml
litellm_settings:
  callbacks: [provenance_callback.callback]
  turn_off_message_logging: true
```

The callback is pinned to 1.102.1 and is copied from the collector's
[reviewed callback and helper](https://github.com/llm-measurement/otelcol-genai-sketches/tree/4e87e262f7362e14585301963e610eba6dcf7a08/examples/integrations/litellm).
It observes raw usage before normalization, marks absent usage unavailable, and
maps validated cache/reasoning subsets without adding them to the totals.
Streaming and unreviewed provider paths remain unknown. The
[validation record](https://github.com/llm-measurement/otelcol-genai-sketches/blob/4e87e262f7362e14585301963e610eba6dcf7a08/examples/integrations/litellm/VALIDATION.md)
defines that scope. Enabling annotations changes instrumentation coverage; do not
interpret that boundary as a workload improvement.

## Verify The Recipes

The SDK checks generate real files and decode their spans, fields, and framing.
The optional Docker checks send those SDK spans through Contrib's actual file
exporter, then run real LiteLLM callbacks against a local synthetic provider.
LiteLLM traffic stays on an internal Docker network; no API key is used and no provider
is billed. Dependencies/images may need downloading before the tests.

```sh
go test ./examples/inspect
go build -o bin/fleetdiff ./cmd/fleetdiff
export FLEETDIFF_BIN="$PWD/bin/fleetdiff"
"$work/venv/bin/python" -m unittest discover -s examples/inspect -p test_capture.py -v
FLEETDIFF_CAPTURE_DOCKER=1 "$work/venv/bin/python" \
  -m unittest discover -s examples/inspect -p test_capture.py -v
```

The tests assert that the stock LiteLLM capture contains unknown-origin `0/0`,
the annotated capture marks it unavailable, subset fields are correct, and
recognizable synthetic prompt/response content is absent. They are integration
checks, not real-provider billing reconciliation or load tests.
With `FLEETDIFF_BIN` set, they also pass SDK and Collector JSON, raw protobuf,
framed protobuf, and LiteLLM captures through `inspect` and check exact counters,
provenance, schema, and the absence of synthetic identifiers in the report.

Accepted inputs are an OTLP JSON request, JSON lines, a raw binary
`ExportTraceServiceRequest`, or the file exporter's length-framed protobuf.
Use `--input-format proto` for a raw request and `--input-format file-proto` for
framed output. Framed records have a four-byte **big-endian** unsigned length,
verified in the pinned [exporter source](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/v0.161.0/exporter/fileexporter/file_writer.go#L40-L48).
Compressed, diagnostic console, and arbitrary vendor JSON are not interchangeable
with these formats. Put only captures in an input directory; it is nonrecursive.
For stdin, use `bin/fleetdiff inspect --input-format json -` (or the matching binary
format). Do not send the same file twice when comparing counts.
