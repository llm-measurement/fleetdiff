# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Synthetic loopback provider and narrowly clock-controlled real proxy runner."""
import argparse
import datetime
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.metadata
import json
import os
from pathlib import Path
import subprocess
import sys
import threading
import time
from types import SimpleNamespace
import urllib.error
import urllib.request

ROOT = Path("/example")
STATE = Path("/tmp/coverage-state.json")
MASTER = "sk-coverage-synthetic-master"
PROVIDER_CALLS = []


def write_state(state):
    temporary = STATE.with_suffix(".tmp")
    temporary.write_text(json.dumps(state))
    temporary.replace(STATE)


def request(path, body=None, key=MASTER, port=4000):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(
        f"http://127.0.0.1:{port}{path}", data=data,
        headers={"Authorization": "Bearer " + key, "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=60) as response:
            return response.status, response.read(), dict(response.headers)
    except urllib.error.HTTPError as error:
        return error.code, error.read(), dict(error.headers)


class Provider(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        state = json.loads(STATE.read_text())
        case = state["case"]
        response_id = "chatcmpl-coverage-" + ("collision" if case.startswith("collision") else case)
        PROVIDER_CALLS.append({"case": case, "response_id": response_id,
                               "stream": bool(body.get("stream"))})
        state["now"] = state["end"]
        write_state(state)
        if case == "failure":
            payload = json.dumps({"error": {"message": "Synthetic provider failure",
                                           "type": "server_error", "code": "mock_failure"}}).encode()
            self.send_response(500)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)
            return
        usage = {"prompt_tokens": 12, "completion_tokens": 3, "total_tokens": 15}
        common = {"id": response_id, "created": 1791331200, "model": "gpt-4o-mini"}
        if body.get("stream"):
            chunks = [dict(common, object="chat.completion.chunk", choices=[
                {"index": 0, "delta": {"role": "assistant", "content": "synthetic ok"},
                 "finish_reason": None}]),
                dict(common, object="chat.completion.chunk", choices=[
                    {"index": 0, "delta": {}, "finish_reason": "stop"}]),
                dict(common, object="chat.completion.chunk", choices=[], usage=usage)]
            payload = ("".join("data: " + json.dumps(chunk) + "\n\n" for chunk in chunks)
                       + "data: [DONE]\n\n").encode()
            content_type = "text/event-stream"
        else:
            payload = json.dumps(dict(common, object="chat.completion", usage=usage,
                choices=[{"index": 0, "message": {"role": "assistant", "content": "synthetic ok"},
                          "finish_reason": "stop"}])).encode()
            content_type = "application/json"
        self.send_response(200)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


def proxy():
    # Replace only LiteLLM request timing references, never the DB writer or queries.
    # Wall/monotonic clocks used by HTTP, retries, Prisma and the scheduler stay real.
    import litellm.utils
    import litellm.litellm_core_utils.litellm_logging as logging_module
    import litellm.proxy.common_request_processing as processing
    import litellm.proxy.hooks.proxy_track_cost_callback as cost_callback
    import litellm.caching.caching_handler as caching

    class RequestDateTime(datetime.datetime):
        @classmethod
        def now(cls, tz=None):
            if not STATE.exists():
                return super().now(tz)
            value = cls.fromisoformat(json.loads(STATE.read_text())["now"])
            return value.astimezone(tz) if tz is not None else value.replace(tzinfo=None)

    clock_module = SimpleNamespace(**{name: getattr(datetime, name) for name in dir(datetime)})
    clock_module.datetime = RequestDateTime
    litellm.utils.datetime = clock_module
    logging_module.datetime = clock_module
    processing.datetime = RequestDateTime
    cost_callback.datetime = RequestDateTime
    caching.datetime = clock_module
    from litellm.proxy.proxy_cli import run_server
    sys.argv = ["litellm", "--config", "/tmp/coverage-config.json", "--host", "127.0.0.1",
                "--port", "4000"]
    run_server()


def serve():
    config = {
        "model_list": [{"model_name": "coverage-model", "litellm_params": {
            "model": "openai/gpt-4o-mini", "api_base": "http://127.0.0.1:8080/v1",
            "api_key": "synthetic-provider-only", "max_retries": 0, "num_retries": 0,
            "timeout": 20}}],
        "router_settings": {"num_retries": 0},
        "litellm_settings": {"num_retries": 0, "cache": True,
                             "cache_params": {"type": "local"}, "turn_off_message_logging": True},
        "general_settings": {"master_key": MASTER, "proxy_batch_write_at": 1,
                             "disable_reset_budget": True}}
    failure_logging = os.environ.get("COVERAGE_FAILURE_LOGGING", "default")
    if failure_logging != "default":
        config["general_settings"]["disable_error_logs"] = failure_logging == "disabled"
    Path("/tmp/coverage-config.json").write_text(json.dumps(config))
    server = ThreadingHTTPServer(("127.0.0.1", 8080), Provider)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    with Path("/tmp/coverage-proxy.log").open("wb") as log:
        child = subprocess.Popen([sys.executable, "-B", __file__, "proxy"], stdout=log, stderr=log)
        try:
            deadline = time.monotonic() + 150
            while True:
                if child.poll() is not None:
                    raise RuntimeError("proxy exited; inspect private coverage-proxy.log")
                try:
                    if request("/health/liveliness")[0] == 200:
                        break
                except OSError:
                    pass
                if time.monotonic() > deadline:
                    raise RuntimeError("proxy startup timed out")
                time.sleep(0.5)
            Path("/tmp/coverage-ready").touch()
            # A separate loopback control endpoint owns the mock oracle, never DB data.
            class Control(BaseHTTPRequestHandler):
                def log_message(self, *args):
                    pass

                def do_GET(self):
                    payload = json.dumps(PROVIDER_CALLS).encode()
                    self.send_response(200)
                    self.end_headers()
                    self.wfile.write(payload)
            ThreadingHTTPServer(("127.0.0.1", 8081), Control).serve_forever()
        finally:
            child.terminate()
            try:
                child.wait(timeout=20)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait()
            server.shutdown()


def exercise(case):
    start = "2026-10-07T12:00:00+00:00"
    end = "2026-10-07T12:00:01+00:00"
    if case == "cross_midnight":
        start, end = "2026-10-07T23:59:59+00:00", "2026-10-08T00:00:01+00:00"
    elif case == "after_midnight":
        start, end = "2026-10-08T00:00:01+00:00", "2026-10-08T00:00:02+00:00"
    write_state({"case": case, "now": start, "end": end})
    key_path = Path("/tmp/coverage-keys.json")
    if not key_path.exists():
        keys = {}
        for label, body in (("user", {"user_id": "coverage-user"}), ("absent", {})):
            status, response, _ = request("/key/generate", dict(body, models=["coverage-model"]))
            if status != 200:
                raise RuntimeError(f"synthetic key creation failed: {status}: {response.decode()}")
            keys[label] = json.loads(response)["key"]
        key_path.write_text(json.dumps(keys))
    keys = json.loads(key_path.read_text())
    before = json.loads(request("/", port=8081)[1])
    cache_case = case in ("cache_fill", "cache_hit")
    body = {"model": "coverage-model", "messages": [{"role": "user", "content":
             "synthetic cache" if cache_case else "synthetic " + case}], "temperature": 0}
    if not cache_case:
        body["cache"] = {"no-cache": True, "no-store": True}
    if case == "stream":
        body.update(stream=True, stream_options={"include_usage": True})
    status, response, headers = request("/v1/chat/completions", body,
        key=keys["absent" if case == "absent_user" else "user"])
    after = json.loads(request("/", port=8081)[1])
    response_ids = []
    if status == 200:
        if case == "stream":
            for line in response.decode().splitlines():
                if line.startswith("data: ") and line != "data: [DONE]":
                    response_ids.append(json.loads(line[6:])["id"])
        else:
            response_ids.append(json.loads(response)["id"])
    print(json.dumps({"case": case, "http_status": status,
                      "start_clock": start, "end_clock": end,
                      "provider_calls": after[len(before):],
                      "response_ids": sorted(set(response_ids)),
                      "cache_header": headers.get("x-litellm-cache-hit"),
                      "stream_done": case == "stream" and b"data: [DONE]" in response}))


def pins():
    base = Path(importlib.metadata.distribution("litellm").locate_file("litellm"))
    files = {"schema": "proxy/schema.prisma", "writer": "proxy/spend_tracking/spend_tracking_utils.py",
             "db_writer": "proxy/db/db_spend_update_writer.py",
             "daily_upsert": "proxy/db/daily_spend_bulk_upsert.py",
             "request_utils": "utils.py", "logging": "litellm_core_utils/litellm_logging.py",
             "request_processing": "proxy/common_request_processing.py",
             "cost_callback": "proxy/hooks/proxy_track_cost_callback.py",
             "caching": "caching/caching_handler.py"}
    print(json.dumps({"version": importlib.metadata.version("litellm"),
                      "source_sha256": {key: hashlib.sha256((base / path).read_bytes()).hexdigest()
                                        for key, path in files.items()}}))


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=("serve", "proxy", "exercise", "pins"))
    parser.add_argument("case", nargs="?")
    args = parser.parse_args()
    if args.mode == "exercise":
        exercise(args.case)
    else:
        globals()[args.mode]()
