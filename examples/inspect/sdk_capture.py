# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Bounded local SDK capture using the public OTLP encoder; no network exporter."""
import argparse
import base64
import json
import os
from pathlib import Path
import struct
import threading

from google.protobuf.json_format import MessageToDict
from opentelemetry.exporter.otlp.proto.common.trace_encoder import encode_spans
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import SimpleSpanProcessor, SpanExporter, SpanExportResult


def otlp_json(request):
    result = MessageToDict(request)
    # OTLP JSON uses hex IDs, unlike the generic protobuf JSON bytes encoding.
    for resource in result.get("resourceSpans", []):
        for scope in resource.get("scopeSpans", []):
            for span in scope.get("spans", []):
                for item in [span, *span.get("links", [])]:
                    for key in ("traceId", "spanId", "parentSpanId"):
                        if key in item:
                            item[key] = base64.b64decode(item[key]).hex()
    return (json.dumps(result, separators=(",", ":")) + "\n").encode()


class LocalOTLPExporter(SpanExporter):
    """Create a private file; stop capturing at the span or byte limit."""

    def __init__(self, path, encoding="file-proto", max_spans=1000, max_bytes=4 * 1024 * 1024):
        if encoding not in ("json", "file-proto"):
            raise ValueError("unsupported capture encoding")
        if not 1 <= max_spans <= 10000 or not 1 <= max_bytes <= 16 * 1024 * 1024:
            raise ValueError("invalid capture limits")
        parent = Path(path).parent.stat()
        if parent.st_uid != os.getuid() or parent.st_mode & 0o077:
            raise ValueError("capture directory must be owned by you with mode 0700")
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        self.file = os.fdopen(fd, "wb")
        self.encoding = encoding
        self.max_spans, self.max_bytes = max_spans, max_bytes
        self.spans = self.bytes = 0
        self.lock = threading.Lock()

    def export(self, spans):
        with self.lock:
            if self.file.closed or self.spans + len(spans) > self.max_spans:
                return SpanExportResult.FAILURE
            request = encode_spans(spans)
            if self.encoding == "json":
                payload = otlp_json(request)
            else:
                body = request.SerializeToString()
                payload = struct.pack(">I", len(body)) + body
            if self.bytes + len(payload) > self.max_bytes:
                return SpanExportResult.FAILURE
            try:
                self.file.write(payload)
                self.file.flush()
            except OSError:
                return SpanExportResult.FAILURE
            self.spans += len(spans)
            self.bytes += len(payload)
            return SpanExportResult.SUCCESS

    def force_flush(self, timeout_millis=30000):
        with self.lock:
            if not self.file.closed:
                self.file.flush()
        return True

    def shutdown(self):
        with self.lock:
            self.file.close()


def make_capture(path, encoding="file-proto", variant="stock"):
    exporter = LocalOTLPExporter(path, encoding)
    provider = TracerProvider(resource=Resource.create({"service.name": "synthetic-inspect-app"}))
    provider.add_span_processor(SimpleSpanProcessor(exporter))
    tracer = provider.get_tracer("fleetdiff.capture-example")
    records = json.loads(Path(__file__).with_name("synthetic-spans.json").read_text())
    try:
        for record in records:
            attributes = record["attributes"]
            if variant == "stock":
                attributes = {k: v for k, v in attributes.items() if not k.startswith("gen_ai_sketch.usage.")}
            with tracer.start_as_current_span(record["name"], attributes=attributes):
                pass
        provider.force_flush()
        if exporter.spans != len(records):
            raise ValueError("capture limit reached")
    finally:
        provider.shutdown()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("output")
    parser.add_argument("--encoding", choices=("file-proto", "json"), default="file-proto")
    parser.add_argument("--variant", choices=("stock", "provenance"), default="stock")
    args = parser.parse_args()
    try:
        make_capture(args.output, args.encoding, args.variant)
    except (OSError, ValueError):
        parser.exit(1, "Cannot write capture; use a new file in a private directory.\n")
    print("Captured six synthetic SDK spans locally. No model calls were made.")
