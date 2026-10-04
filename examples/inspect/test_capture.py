# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Real SDK captures; set FLEETDIFF_CAPTURE_DOCKER=1 for pinned-image checks."""
import base64
from contextlib import contextmanager
import http.client
import json
import os
from pathlib import Path
import stat
import struct
import subprocess
import tempfile
import time
import unittest
import urllib.error
import urllib.request
import uuid

from google.protobuf.json_format import ParseDict
from opentelemetry.proto.collector.trace.v1.trace_service_pb2 import ExportTraceServiceRequest
from opentelemetry.sdk.trace.export import SpanExportResult

from sdk_capture import LocalOTLPExporter, make_capture

ROOT = Path(__file__).resolve().parent
COLLECTOR = "otel/opentelemetry-collector-contrib@sha256:fd328de2552466ad78385e1b1289c3f2402b1c45f265b252aab1955b42845ac1"
LITELLM = "ghcr.io/berriai/litellm@sha256:87f34979b9f8cb274fac90ca8a4fdda07d8480de22755562a26adeb95ce20d02"
CLI = os.environ.get("FLEETDIFF_BIN")


def check_inspection(test, path, encoding, *, attempts, missing, input_tokens, output_tokens, unknown):
    if not CLI:
        return
    result = subprocess.run([CLI, "inspect", "--format", "json", "--input-format", encoding, str(path)],
                            capture_output=True, text=True, timeout=30)
    test.assertEqual(result.returncode, 0, result.stderr)
    test.assertNotIn("SYNTHETIC_", result.stdout + result.stderr)
    test.assertNotIn(str(path), result.stdout + result.stderr)
    report = json.loads(result.stdout)
    test.assertEqual(report["schema"], "fleetdiff-inspect/v1")
    counters = report["metrics"]
    for name, expected in {"requests_total": attempts, "missing_token_usage_total": missing,
                           "input_tokens_total": input_tokens, "output_tokens_total": output_tokens,
                           "total_tokens_total": input_tokens + output_tokens}.items():
        test.assertEqual(counters.get("gen_ai_sketch_" + name, 0), expected, name)
    test.assertEqual(report["usage_provenance"].get("input/unknown", 0), unknown)
    test.assertEqual(report["usage_provenance"].get("output/unknown", 0), unknown)
    test.assertGreaterEqual(report["readiness"]["total"], 6)
    test.assertLessEqual(report["readiness"]["ready"], report["readiness"]["total"])
    return report


def requests(path, encoding):
    data = path.read_bytes()
    result = []
    if encoding == "json":
        for line in data.splitlines():
            item = json.loads(line)
            # Generic protobuf JSON expects base64 bytes; OTLP JSON uses hex IDs.
            for resource in item.get("resourceSpans", []):
                for scope in resource.get("scopeSpans", []):
                    for span in scope.get("spans", []):
                        for key in ("traceId", "spanId", "parentSpanId"):
                            if key in span:
                                span[key] = base64.b64encode(bytes.fromhex(span[key])).decode()
            result.append(ParseDict(item, ExportTraceServiceRequest()))
    else:
        while data:
            if len(data) < 4:
                raise ValueError("truncated frame header")
            size = struct.unpack(">I", data[:4])[0]
            if not 0 < size <= len(data) - 4:
                raise ValueError("invalid frame size")
            item = ExportTraceServiceRequest()
            item.ParseFromString(data[4:4 + size])
            result.append(item)
            data = data[4 + size:]
    return result


def spans(path, encoding):
    return [span for request in requests(path, encoding)
            for resource in request.resource_spans for scope in resource.scope_spans for span in scope.spans]


def attributes(span):
    return {a.key: getattr(a.value, a.value.WhichOneof("value")) for a in span.attributes}


def docker(*args, timeout=90):
    result = subprocess.run(["docker", *map(str, args)], capture_output=True, text=True, timeout=timeout)
    if result.returncode:
        raise RuntimeError("Synthetic Docker capture failed: " + result.stderr[-3000:])
    return result.stdout.strip()


@contextmanager
def collector(output, encoding="json", isolated=False):
    network = "fleetdiff-capture-" + uuid.uuid4().hex
    output.mkdir(mode=0o700)
    container = None
    docker("network", "create", *(["--internal"] if isolated else []), network)
    try:
        options = ["--config=/config/collector.yaml"]
        if encoding == "file-proto":
            override = output.parent / "override.json"
            override.write_text(json.dumps({"exporters": {"file/capture": {"format": "proto"}}}))
            options.append("--config=/override.json")
            mounts = ["--mount", f"type=bind,src={override},dst=/override.json,readonly"]
        else:
            mounts = []
        publish = [] if isolated else ["-p", "127.0.0.1::4318"]
        container = docker("run", "-d", "--network", network, "--network-alias", "capture",
                           "--user", f"{os.getuid()}:{os.getgid()}", "--read-only", "--cap-drop=ALL",
                           "--security-opt=no-new-privileges", "--memory=256m", "--pids-limit=128",
                           "--log-driver=local", "--log-opt=max-size=1m", *publish,
                           "--mount", f"type=bind,src={ROOT},dst=/config,readonly",
                           "--mount", f"type=bind,src={output},dst=/capture", *mounts, COLLECTOR, *options)
        if isolated:
            yield None, network
            return
        port = docker("port", container, "4318/tcp").rsplit(":", 1)[1]
        endpoint = f"http://127.0.0.1:{port}/v1/traces"
        deadline = time.monotonic() + 15
        while True:
            try:
                send(endpoint, b'{"resourceSpans":[]}')
                break
            except (urllib.error.URLError, TimeoutError, http.client.RemoteDisconnected):
                if time.monotonic() > deadline:
                    raise RuntimeError("capture collector did not start") from None
                time.sleep(0.1)
        yield endpoint, network
    finally:
        if container:
            docker("stop", "--time", "10", container)
            docker("rm", container)
        docker("network", "rm", network)


def send(endpoint, data):
    request = urllib.request.Request(endpoint, data=data, headers={"Content-Type": "application/json"})
    # Ignore workstation HTTP proxy variables for this loopback-only test.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with opener.open(request, timeout=3) as response:
        result = json.loads(response.read())
        if result.get("partialSuccess", {}).get("rejectedSpans", "0") not in (0, "0"):
            raise ValueError("collector rejected synthetic spans")


class SDKCaptureTests(unittest.TestCase):
    def test_real_sdk_formats_and_accounting_inputs(self):
        for encoding in ("json", "file-proto"):
            for variant in ("stock", "provenance"):
                with self.subTest(encoding=encoding, variant=variant), tempfile.TemporaryDirectory() as temp:
                    path = Path(temp) / "capture"
                    make_capture(path, encoding, variant)
                    self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
                    records = [attributes(s) for s in spans(path, encoding)]
                    self.assertEqual(len(records), 6)
                    model = [a for a in records if a["gen_ai.operation.name"] == "chat"]
                    self.assertEqual(len(model), 4)
                    self.assertEqual(sum(a.get("gen_ai.usage.input_tokens", 0) for a in model), 140)
                    self.assertEqual(sum(a.get("gen_ai.usage.output_tokens", 0) for a in model), 30)
                    self.assertEqual(model[0]["gen_ai.usage.cache_read.input_tokens"], 25)
                    self.assertEqual(model[0]["gen_ai.usage.reasoning.output_tokens"], 5)
                    declarations = sum(k.startswith("gen_ai_sketch.usage.") for a in records for k in a)
                    self.assertEqual(declarations, 6 if variant == "provenance" else 0)

    def test_private_exclusive_bounded_file(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "capture"
            exporter = LocalOTLPExporter(path, max_bytes=1)
            self.assertEqual(exporter.export([]), SpanExportResult.FAILURE)
            exporter.shutdown()
            self.assertEqual(path.read_bytes(), b"")
            with self.assertRaises(FileExistsError):
                LocalOTLPExporter(path)
            path.unlink()
            path.symlink_to(Path(temp) / "other")
            with self.assertRaises(FileExistsError):
                LocalOTLPExporter(path)
            path.unlink()
            Path(temp).chmod(0o755)
            with self.assertRaises(ValueError):
                LocalOTLPExporter(path)


@unittest.skipUnless(CLI, "set FLEETDIFF_BIN to the built inspect CLI")
class CLIExampleTests(unittest.TestCase):
    def test_sdk_captures_roundtrip_through_inspect(self):
        for encoding in ("json", "file-proto"):
            for variant in ("stock", "provenance"):
                with self.subTest(encoding=encoding, variant=variant), tempfile.TemporaryDirectory() as temp:
                    path = Path(temp) / "capture"
                    make_capture(path, encoding, variant)
                    check_inspection(self, path, encoding, attempts=4,
                                     missing=2 if variant == "provenance" else 1,
                                     input_tokens=140, output_tokens=30,
                                     unknown=1 if variant == "provenance" else 4)

    def test_raw_protobuf_and_json_stdin(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            framed = root / "sdk.frames"
            make_capture(framed, "file-proto", "provenance")
            request = ExportTraceServiceRequest()
            for part in requests(framed, "file-proto"):
                request.resource_spans.extend(part.resource_spans)
            raw = root / "sdk.pb"
            raw.write_bytes(request.SerializeToString())
            check_inspection(self, raw, "proto", attempts=4, missing=2,
                             input_tokens=140, output_tokens=30, unknown=1)
            capture = root / "sdk.jsonl"
            make_capture(capture, "json", "provenance")
            result = subprocess.run([CLI, "inspect", "--format", "json", "--input-format", "json", "-"],
                                    input=capture.read_bytes(), capture_output=True, timeout=30)
            self.assertEqual(result.returncode, 0, result.stderr)
            report = json.loads(result.stdout)
            self.assertEqual(report["metrics"]["gen_ai_sketch_requests_total"], 4)
            self.assertEqual(report["capture"]["spans"], 6)
            self.assertNotIn(b"SYNTHETIC_", result.stdout + result.stderr)


@unittest.skipUnless(os.environ.get("FLEETDIFF_CAPTURE_DOCKER") == "1", "opt-in Docker capture check")
class DockerCaptureTests(unittest.TestCase):
    def test_capture_script_from_another_directory(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            output = root / "private-capture"
            sdk_file = root / "sdk.jsonl"
            make_capture(sdk_file, "json", "provenance")
            process = subprocess.Popen(["sh", str(ROOT / "capture.sh"), str(output), "4"],
                                       cwd=root, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            try:
                self.assertIn("Capturing for", process.stdout.readline())
                endpoint = "http://127.0.0.1:14318/v1/traces"
                deadline = time.monotonic() + 2
                while True:
                    try:
                        send(endpoint, b'{"resourceSpans":[]}')
                        break
                    except (urllib.error.URLError, TimeoutError, http.client.RemoteDisconnected):
                        if time.monotonic() > deadline:
                            raise
                        time.sleep(0.1)
                for line in sdk_file.read_bytes().splitlines():
                    send(endpoint, line)
                _, errors = process.communicate(timeout=30)
                self.assertEqual(process.returncode, 0, errors)
            finally:
                if process.poll() is None:
                    process.terminate()
                    process.communicate(timeout=30)
            path = output / "traces.jsonl"
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
            self.assertEqual(len(spans(path, "json")), 6)
            check_inspection(self, path, "json", attempts=4, missing=2,
                             input_tokens=140, output_tokens=30, unknown=1)

    def test_official_exporter_json_and_big_endian_protobuf(self):
        for encoding in ("json", "file-proto"):
            with self.subTest(encoding=encoding), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                sdk_file = root / "sdk.jsonl"
                make_capture(sdk_file, "json", "provenance")
                output = root / "capture"
                with collector(output, encoding) as (endpoint, _):
                    for line in sdk_file.read_bytes().splitlines():
                        send(endpoint, line)
                records = [attributes(s) for s in spans(output / "traces.jsonl", encoding)]
                self.assertEqual(len(records), 6)
                self.assertEqual(records, [attributes(s) for s in spans(sdk_file, "json")])
                self.assertEqual(stat.S_IMODE(output.stat().st_mode), 0o700)
                check_inspection(self, output / "traces.jsonl", encoding, attempts=4, missing=2,
                                 input_tokens=140, output_tokens=30, unknown=1)

    def test_real_litellm_stock_and_provenance(self):
        for variant in ("stock", "provenance"):
            with self.subTest(variant=variant), tempfile.TemporaryDirectory() as temp:
                output = Path(temp) / "capture"
                config = Path(temp) / "litellm.json"
                config.write_text(json.dumps({
                    "model_list": [{"model_name": case, "litellm_params": {
                        "model": "openai/gpt-4o-mini", "api_base": f"http://provider:8080/{case}/v1",
                        "api_key": "synthetic-only", "num_retries": 0, "timeout": 10,
                    }} for case in ("reported", "unavailable")],
                    "litellm_settings": {"callbacks": ["provenance_callback.callback" if variant == "provenance" else "otel"],
                                         "turn_off_message_logging": True},
                    "general_settings": {"master_key": "sk-synthetic-only", "allow_client_side_credentials": False},
                }))
                with collector(output, isolated=True) as (_, network):
                    restricted = ["--network", network, "--read-only", "--cap-drop=ALL",
                           "--security-opt=no-new-privileges", "--pids-limit=256", "--memory=1g",
                           "--user", f"{os.getuid()}:{os.getgid()}", "--tmpfs", "/tmp:rw,noexec,nosuid,size=128m",
                           "--mount", f"type=bind,src={ROOT / 'litellm'},dst=/capture-example,readonly",
                           "-e", "HOME=/tmp", "-e", "PYTHONPATH=/capture-example", "-e", "PYTHONDONTWRITEBYTECODE=1",
                           "-e", f"CAPTURE_VARIANT={variant}", "-e", "LITELLM_LOCAL_MODEL_COST_MAP=True",
                           "-e", "LITELLM_TELEMETRY=False", "-e", "OTEL_EXPORTER=otlp_http",
                           "-e", "OTEL_ENDPOINT=http://capture:4318", "-e", "OTEL_BSP_SCHEDULE_DELAY=100",
                           "-e", "OTEL_SEMCONV_STABILITY_OPT_IN=gen_ai_latest_experimental",
                           "-e", "OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=NO_CONTENT"]
                    proxy = docker("run", "-d", *restricted, "--network-alias", "proxy",
                                   "--mount", f"type=bind,src={config},dst=/proxy.json,readonly",
                                   "--log-driver=none", "--entrypoint", "litellm", LITELLM,
                                   "--config", "/proxy.json", "--host", "0.0.0.0", "--port", "4000")
                    try:
                        docker("run", "--rm", *restricted, "--network-alias", "provider",
                               "-e", "CAPTURE_PROXY=http://proxy:4000", "--entrypoint", "python", LITELLM,
                               "/capture-example/synthetic_client.py")
                    finally:
                        docker("stop", "--time", "10", proxy)
                        docker("rm", proxy)
                path = output / "traces.jsonl"
                data = path.read_bytes()
                self.assertNotIn(b"SYNTHETIC_PROMPT_NO_EXPORT", data)
                self.assertNotIn(b"SYNTHETIC_RESPONSE_NO_EXPORT", data)
                model = [attributes(s) for s in spans(path, "json")
                         if "gen_ai.request.model" in attributes(s)]
                self.assertEqual(len(model), 2, [
                    {"operation": attributes(s).get("gen_ai.operation.name"), "keys": sorted(attributes(s))}
                    for s in spans(path, "json")])
                self.assertTrue(all(a.get("gen_ai.operation.name") in (None, "chat") for a in model))
                self.assertEqual(sorted(a.get("gen_ai.usage.input_tokens") for a in model), [0, 100])
                self.assertEqual(sorted(a.get("gen_ai.usage.output_tokens") for a in model), [0, 20])
                origins = sorted(a.get("gen_ai_sketch.usage.input.provenance", "unknown") for a in model)
                self.assertEqual(origins, ["provider_reported", "unavailable"] if variant == "provenance" else ["unknown", "unknown"])
                if variant == "provenance":
                    reported = next(a for a in model if a["gen_ai.usage.input_tokens"] == 100)
                    self.assertEqual(reported["gen_ai.usage.cache_read.input_tokens"], 25)
                    self.assertEqual(reported["gen_ai.usage.reasoning.output_tokens"], 5)
                check_inspection(self, path, "json", attempts=2,
                                 missing=1 if variant == "provenance" else 0,
                                 input_tokens=100, output_tokens=20,
                                 unknown=0 if variant == "provenance" else 2)


if __name__ == "__main__":
    unittest.main()
