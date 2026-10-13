# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Bounded local capture with an already-installed collector; Python stdlib only."""
import argparse
import hashlib
import http.client
from http.server import BaseHTTPRequestHandler, HTTPServer
import json
import os
from pathlib import Path
import signal
import stat
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parent
IMAGE = "otel/opentelemetry-collector-contrib@sha256:39923a8e431bd1f57be82411999d389fcfe40857492e4365456d97a4c1f74be6"
RELAY_IMAGE = "python@sha256:593bd06efe90efa80dc4eee3948be7c0fde4134606dd40d8dd8dbcade98e669c"
MAX_BYTES = 8 * 1024 * 1024
MAX_REQUEST = 1024 * 1024
RELAY = '''import sys, urllib.request
request = urllib.request.Request("http://127.0.0.1:4318/v1/logs", data=sys.stdin.buffer.read(1048577),
    headers={"Content-Type": sys.argv[1]})
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
with opener.open(request, timeout=3) as response:
    sys.stdout.buffer.write(response.read(65536))
'''


def docker(*args):
    result = subprocess.run(["docker", *map(str, args)], capture_output=True, text=True, timeout=30)
    if result.returncode:
        raise RuntimeError("Local collector operation failed; no image was downloaded.")
    return result.stdout.strip()


def send(endpoint, data):
    request = urllib.request.Request(endpoint, data=data, headers={"Content-Type": "application/json"})
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with opener.open(request, timeout=2) as response:
        value = json.loads(response.read(65536))
    partial = value.get("partialSuccess", {})
    if partial.get("rejectedLogRecords", "0") not in (0, "0") or partial.get("errorMessage"):
        raise RuntimeError("Collector reported partial acceptance.")


class Capture:
    def __init__(self, directory, max_bytes=MAX_BYTES):
        self.directory = Path(directory).absolute()
        self.max_bytes = max_bytes
        self.name = "fleetdiff-claude-" + uuid.uuid4().hex
        self.network_created = False
        self.container_created = False
        self.sender_created = False
        self.server = None
        self.thread = None
        self.endpoint = None

    def start(self):
        docker("image", "inspect", IMAGE)
        docker("image", "inspect", RELAY_IMAGE)
        self.directory.mkdir(mode=0o700)
        os.chmod(self.directory, 0o700)
        with (self.directory / "logs.jsonl").open("xb") as output:
            os.fchmod(output.fileno(), 0o600)
        try:
            docker("network", "create", "--internal", self.name)
            self.network_created = True
            docker("create", "--name", self.name, "--pull", "never", "--network", self.name,
                   "--user", f"{os.getuid()}:{os.getgid()}", "--read-only", "--cap-drop=ALL",
                   "--security-opt=no-new-privileges", "--memory=256m", "--pids-limit=128",
                   "--ulimit", f"fsize={self.max_bytes}:{self.max_bytes}", "--log-driver=none",
                   "--mount", f"type=bind,src={ROOT / 'collector.yaml'},dst=/etc/capture.yaml,readonly",
                   "--mount", f"type=bind,src={self.directory},dst=/capture", IMAGE,
                   "--config=/etc/capture.yaml")
            self.container_created = True
            docker("start", self.name)
            docker("create", "--name", self.name + "-relay", "--pull", "never",
                   "--network", "container:" + self.name, "--user", f"{os.getuid()}:{os.getgid()}",
                   "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges",
                   "--memory=64m", "--pids-limit=32", "--log-driver=none", RELAY_IMAGE,
                   "python", "-B", "-c", "import time; time.sleep(600)")
            self.sender_created = True
            docker("start", self.name + "-relay")
            self.start_relay()
            deadline = time.monotonic() + 15
            while True:
                try:
                    send(self.endpoint, b'{"resourceLogs":[]}')
                    return self.endpoint
                except (urllib.error.URLError, TimeoutError, ConnectionError, http.client.RemoteDisconnected):
                    if time.monotonic() >= deadline or not self.running():
                        raise RuntimeError("Capture collector did not become ready.") from None
                    time.sleep(0.1)
        except BaseException:
            self.stop()
            raise

    def start_relay(self):
        session = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_args):
                pass

            def setup(self):
                super().setup()
                self.connection.settimeout(5)

            def do_POST(self):
                status, output = 400, b'{}'
                try:
                    size = int(self.headers.get("Content-Length", "-1"))
                    content_type = self.headers.get("Content-Type", "")
                    if (self.path != "/v1/logs" or self.headers.get("Content-Encoding")
                            or self.headers.get("Transfer-Encoding") or not 0 <= size <= MAX_REQUEST
                            or content_type != "application/json"):
                        status = 413
                    else:
                        data = self.rfile.read(size)
                        if len(data) == size:
                            result = subprocess.run(["docker", "exec", "-i", session.name + "-relay",
                                "python", "-B", "-c", RELAY, content_type], input=data,
                                capture_output=True, timeout=8)
                            status = 200 if result.returncode == 0 else 502
                            # Only the response status is exposed, never collector error text.
                            if status == 200:
                                partial = json.loads(result.stdout or b'{}').get("partialSuccess", {})
                                if partial.get("rejectedLogRecords", "0") not in (0, "0") or partial.get("errorMessage"):
                                    status = 502
                except (ValueError, OSError, subprocess.SubprocessError):
                    status = 502
                self.send_response(status)
                self.send_header("Content-Length", str(len(output)))
                self.end_headers()
                try:
                    self.wfile.write(output)
                except OSError:
                    pass

        self.server = HTTPServer(("127.0.0.1", 0), Handler)
        self.endpoint = f"http://127.0.0.1:{self.server.server_port}/v1/logs"
        self.thread = threading.Thread(target=self.server.serve_forever, kwargs={"poll_interval": 0.1}, daemon=True)
        self.thread.start()

    def running(self):
        return docker("inspect", "--format", "{{.State.Running}}", self.name) == "true"

    def stop(self):
        errors = []
        if self.server:
            self.server.shutdown()
            self.server.server_close()
            self.thread.join(timeout=10)
            self.server = None
        if self.sender_created:
            try:
                docker("rm", "--force", self.name + "-relay")
                self.sender_created = False
            except RuntimeError as error:
                errors.append(error)
        if self.container_created:
            try:
                docker("stop", "--time", "10", self.name)
            except RuntimeError as error:
                errors.append(error)
            try:
                docker("rm", "--force", self.name)
                self.container_created = False
            except RuntimeError as error:
                errors.append(error)
        if self.network_created:
            try:
                docker("network", "rm", self.name)
                self.network_created = False
            except RuntimeError as error:
                errors.append(error)
        if errors:
            raise RuntimeError("Capture cleanup failed; check local Docker resources.")

    def finalize(self, reason):
        path = self.directory / "logs.jsonl"
        size = path.stat().st_size
        valid = size < self.max_bytes and reason not in ("size-cap", "collector-stopped")
        records = 0
        try:
            for line in path.read_bytes().splitlines():
                batch = json.loads(line)
                records += sum(len(scope.get("logRecords", []))
                               for resource in batch.get("resourceLogs", [])
                               for scope in resource.get("scopeLogs", []))
        except (ValueError, TypeError, AttributeError):
            valid = False
        private = (stat.S_IMODE(path.stat().st_mode) == 0o600
                   and stat.S_IMODE(self.directory.stat().st_mode) == 0o700)
        metadata = {"profile": "claude-code-logs/v1", "image": IMAGE, "relay_image": RELAY_IMAGE,
                    "config_sha256": hashlib.sha256((ROOT / "collector.yaml").read_bytes()).hexdigest(),
                    "capture_sha256": hashlib.sha256(path.read_bytes()).hexdigest(),
                    "bytes": size, "byte_cap": self.max_bytes, "observed_records": records,
                    "stop_reason": reason, "valid_jsonl_below_cap": valid, "private_modes": private,
                    "finality": "unknown", "completeness": "unknown"}
        with (self.directory / "capture.json").open("x", encoding="ascii") as output:
            os.fchmod(output.fileno(), 0o600)
            json.dump(metadata, output, indent=2)
            output.write("\n")
        if not valid or not private or reason == "collector-stopped":
            raise RuntimeError("Capture is incomplete or invalid; do not import it as a complete window.")
        return metadata


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path, help="new private directory outside the repository")
    parser.add_argument("--seconds", type=int, default=30, help="capture duration, 1-300 seconds")
    args = parser.parse_args()
    if not 1 <= args.seconds <= 300:
        parser.error("duration must be 1-300 seconds")
    if ROOT.parents[1] in args.directory.resolve().parents:
        parser.error("capture must be outside the repository")
    os.umask(0o077)
    session = Capture(args.directory)
    interrupted = False

    def interrupt(_signum, _frame):
        nonlocal interrupted
        interrupted = True

    for signum in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
        signal.signal(signum, interrupt)
    reason = "timer"
    exit_code = 0
    try:
        endpoint = session.start()
        print(f"OTLP logs endpoint: {endpoint}", flush=True)
        print(f"Capture stops after {args.seconds} seconds; raw attribution IDs remain private.", flush=True)
        deadline = time.monotonic() + args.seconds
        while time.monotonic() < deadline and not interrupted:
            if not session.running():
                reason = "collector-stopped"
                break
            if (session.directory / "logs.jsonl").stat().st_size >= MAX_BYTES:
                reason = "size-cap"
                break
            time.sleep(0.2)
        if interrupted:
            reason = "signal"
        session.stop()
        session.finalize(reason)
        print("Capture stopped and flushed. Review privately, then delete the capture directory.")
    except (OSError, RuntimeError, subprocess.SubprocessError):
        print("Capture failed. Keep its files private; do not assume complete coverage.", file=sys.stderr)
        exit_code = 1
    finally:
        try:
            session.stop()
        except (RuntimeError, subprocess.SubprocessError):
            print("Capture cleanup failed; check local Docker resources.", file=sys.stderr)
            exit_code = 1
    return exit_code


if __name__ == "__main__":
    sys.exit(main())
