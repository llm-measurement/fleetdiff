# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Exercise the real LiteLLM callbacks with an in-process synthetic provider."""
import http.server
import json
import os
import threading
import time
import urllib.error
import urllib.request
from importlib.metadata import version

class Provider(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        if not 0 < length <= 65536:
            self.send_error(400)
            return
        self.rfile.read(length)
        body = {
            "id": "synthetic-request", "object": "chat.completion", "created": 1700000000,
            "model": "gpt-4o-mini",
            "choices": [{"index": 0, "message": {"role": "assistant", "content": "SYNTHETIC_RESPONSE_NO_EXPORT"}, "finish_reason": "stop"}],
        }
        if self.path.startswith("/reported/"):
            body["usage"] = {"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120,
                             "prompt_tokens_details": {"cached_tokens": 25},
                             "completion_tokens_details": {"reasoning_tokens": 5}}
        payload = json.dumps(body).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


if __name__ == "__main__":
    if version("litellm") != "1.102.1":
        raise SystemExit("This capture test requires LiteLLM 1.102.1.")
    # The test proxy and provider share only an internal Docker network.
    proxy = os.environ["CAPTURE_PROXY"]
    if proxy != "http://proxy:4000":
        raise SystemExit("Use the isolated synthetic proxy test.")
    server = http.server.ThreadingHTTPServer(("0.0.0.0", 8080), Provider)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        deadline = time.monotonic() + 45
        while True:
            try:
                with opener.open(proxy + "/health/liveliness", timeout=2):
                    break
            except (urllib.error.URLError, TimeoutError, ConnectionError):
                if time.monotonic() >= deadline:
                    raise RuntimeError("synthetic proxy did not start") from None
                time.sleep(0.25)
        for case in ("reported", "unavailable"):
            messages = [{"role": "user", "content": "SYNTHETIC_PROMPT_NO_EXPORT"}]
            data = json.dumps({"model": case, "messages": messages, "user": "SYNTHETIC_CAPTURE_USER", "stream": False}).encode()
            request = urllib.request.Request(proxy + "/v1/chat/completions", data,
                                             {"Content-Type": "application/json", "Authorization": "Bearer sk-synthetic-only"})
            with opener.open(request, timeout=15) as response:
                if response.status != 200:
                    raise RuntimeError("synthetic request failed")
        # LiteLLM invokes success callbacks on a background worker.
        time.sleep(3)
    finally:
        server.shutdown()
        server.server_close()
    print("Completed two synthetic model attempts; no provider key or model bill.")
