# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Capture-only conformance. No client, importer, credentials or paid endpoints."""
import hashlib
import http.client
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import unittest
from unittest import mock
import urllib.error

from capture import Capture, IMAGE, MAX_REQUEST, RELAY_IMAGE, ROOT, docker, send
import fixtures

DOCKER = os.environ.get("FLEETDIFF_CLAUDE_CAPTURE_DOCKER") == "1"
CLI = os.environ.get("FLEETDIFF_CLAUDE_CAPTURE_CLI") == "1"
EVIDENCE = None


def evidence_root():
    global EVIDENCE
    if EVIDENCE is None:
        requested = os.environ.get("FLEETDIFF_CAPTURE_EVIDENCE")
        if requested:
            EVIDENCE = Path(requested).resolve()
            if ROOT.parents[1] == EVIDENCE or ROOT.parents[1] in EVIDENCE.parents:
                raise ValueError("test evidence must be outside the repository")
            EVIDENCE.mkdir(mode=0o700)
        else:
            EVIDENCE = Path(tempfile.mkdtemp(prefix="fleetdiff-claude-capture-"))
        EVIDENCE.chmod(0o700)
    return EVIDENCE


def private_json(path, data):
    with path.open("x", encoding="ascii") as output:
        os.fchmod(output.fileno(), 0o600)
        json.dump(data, output, indent=2)
        output.write("\n")


def records(path):
    return [(resource, scope, log)
            for line in path.read_bytes().splitlines()
            for resource in json.loads(line).get("resourceLogs", [])
            for scope in resource.get("scopeLogs", [])
            for log in scope.get("logRecords", [])]


def attrs(items):
    return {item["key"]: item["value"] for item in items}


def investigate(path, group="user", output_format="json", deduplicate=False):
    environment = dict(os.environ)
    binary = environment.get("FLEETDIFF_BIN")
    if binary:
        command = [str(Path(binary).resolve())]
    else:
        environment.setdefault("GOTOOLCHAIN", "local")
        environment.setdefault("GOPROXY", "off")
        environment.setdefault("GOCACHE", str(evidence_root() / "go-cache"))
        command = ["go", "run", "./cmd/fleetdiff"]
    command += ["investigate", "--claude-code-logs", str(path),
               "--before-period", "2026-10-11T00:00:00Z/2026-10-12T00:00:00Z",
               "--after-period", "2026-10-12T00:00:00Z/2026-10-13T00:00:00Z",
               "--group-by", group, "--format", output_format]
    if deduplicate:
        command.append("--deduplicate-exact")
    return subprocess.run(command, cwd=ROOT.parents[1], env=environment,
                          capture_output=True, text=True, timeout=120)


class FixtureTests(unittest.TestCase):
    def test_cli_uses_supplied_binary_and_preserves_ci_environment(self):
        with mock.patch.dict(os.environ, {"FLEETDIFF_BIN": "/ci/bin/fleetdiff", "GOCACHE": "/ci/cache",
                                         "GOTOOLCHAIN": "local", "GOSUMDB": "sum.golang.org"}), \
                mock.patch("subprocess.run") as run:
            investigate(Path("fixture.jsonl"))
            self.assertEqual(run.call_args.args[0][0], "/ci/bin/fleetdiff")
            self.assertEqual(run.call_args.kwargs["env"]["GOCACHE"], "/ci/cache")
            self.assertEqual(run.call_args.kwargs["env"]["GOTOOLCHAIN"], "local")
            self.assertEqual(run.call_args.kwargs["env"]["GOSUMDB"], "sum.golang.org")

    def test_deterministic_fresh_fixture_and_profile(self):
        self.assertEqual(fixtures.clean(), fixtures.clean())
        batch = fixtures.clean()["resourceLogs"][0]
        self.assertEqual(attrs(batch["resource"]["attributes"])["fleetdiff.capture.profile"],
                         {"stringValue": "claude-code-logs/v1"})
        request = batch["scopeLogs"][0]["logRecords"][0]
        self.assertEqual(request["timeUnixNano"], "1791676800000000000")
        self.assertEqual(attrs(request["attributes"])["cost_usd"], {"doubleValue": 0.00003435})
        self.assertEqual(attrs(request["attributes"])["model"], {"stringValue": "synthetic-model-a"})

    def test_existing_directory_and_symlink_are_rejected(self):
        with tempfile.TemporaryDirectory() as temporary, mock.patch("capture.docker", return_value=""):
            path = Path(temporary)
            with self.assertRaises(FileExistsError):
                Capture(path).start()
            link = path / "link"
            link.symlink_to(path, target_is_directory=True)
            with self.assertRaises(FileExistsError):
                Capture(link).start()

    def test_cli_rejects_invalid_duration_without_docker(self):
        result = subprocess.run([sys.executable, "-B", str(ROOT / "capture.py"),
                                 "/unused", "--seconds", "0"], capture_output=True, timeout=10)
        self.assertEqual(result.returncode, 2)

    def test_partial_acceptance_is_failure(self):
        response = mock.MagicMock()
        response.__enter__.return_value.read.return_value = b'{"partialSuccess":{"rejectedLogRecords":"1"}}'
        with mock.patch("urllib.request.build_opener") as opener:
            opener.return_value.open.return_value = response
            with self.assertRaisesRegex(RuntimeError, "partial acceptance"):
                send("http://127.0.0.1:1/v1/logs", b"{}")

    def test_relay_rejects_unbounded_encoded_and_nonlog_requests(self):
        session = Capture("/unused")
        session.start_relay()
        try:
            with mock.patch("capture.subprocess.run") as execute:
                for path, headers in [
                    ("/v1/traces", {"Content-Length": "0"}),
                    ("/v1/logs", {"Content-Length": str(MAX_REQUEST + 1)}),
                    ("/v1/logs", {"Content-Length": "0", "Content-Encoding": "gzip"}),
                    ("/v1/logs", {"Content-Length": "0", "Content-Type": "application/x-protobuf"}),
                    ("/v1/logs", {"Content-Length": "0", "Transfer-Encoding": "chunked"}),
                ]:
                    connection = http.client.HTTPConnection(*session.server.server_address, timeout=3)
                    connection.request("POST", path, headers={"Content-Type": "application/json", **headers})
                    self.assertEqual(connection.getresponse().status, 413)
                    connection.close()
                execute.assert_not_called()
        finally:
            session.stop()


@unittest.skipUnless(CLI, "set FLEETDIFF_CLAUDE_CAPTURE_CLI=1 for the local-checkout smoke check")
class CLIExampleTests(unittest.TestCase):
    def test_generated_model_fixture_user_session_and_text(self):
        path = evidence_root() / "cli-fixture.jsonl"
        generated = subprocess.run([sys.executable, "-B", str(ROOT / "fixtures.py"), "--output", str(path)],
                                   capture_output=True, text=True, timeout=10)
        self.assertEqual(generated.returncode, 0, generated.stderr)
        for group in ("user", "session"):
            result = investigate(path, group=group)
            self.assertEqual(result.returncode, 0, result.stderr)
            report = json.loads(result.stdout)
            self.assertEqual(report["unit"], "observed api_request records")
            self.assertEqual(report["finality"], "unknown")
            self.assertTrue(report["models"])
            for period in ("before", "after"):
                self.assertEqual(report[period]["observed_api_request_records"], 1)
                self.assertEqual(report[period]["inclusive_input"]["tokens"], 23)
                self.assertEqual(report[period]["output"]["tokens"], 4)
                self.assertEqual(report[period]["recorded_cost_usd"], "0.00003435")
            self.assertEqual(report["after"]["api_error_events"], 1)
            self.assertEqual(report["after"]["api_retries_exhausted_events"], 1)
            private_json(evidence_root() / ("cli-" + group + ".json"), report)
        result = investigate(path, output_format="text")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("unknown", result.stdout)


@unittest.skipUnless(DOCKER, "set FLEETDIFF_CLAUDE_CAPTURE_DOCKER=1; pinned image must already be local")
class CollectorTests(unittest.TestCase):
    def capture(self, name, batches, max_bytes=8 * 1024 * 1024):
        root = evidence_root() / name
        root.mkdir(mode=0o700)
        private_json(root / "input.json", batches)
        session = Capture(root / "capture", max_bytes=max_bytes)
        try:
            endpoint = session.start()
            network = json.loads(docker("network", "inspect", session.name))[0]
            container = json.loads(docker("inspect", session.name))[0]
            self.assertTrue(network["Internal"])
            self.assertFalse(container["HostConfig"]["PortBindings"])
            self.assertEqual(session.server.server_address[0], "127.0.0.1")
            relay = json.loads(docker("inspect", session.name + "-relay"))[0]
            self.assertEqual(relay["HostConfig"]["NetworkMode"], "container:" + container["Id"])
            self.assertEqual(container["HostConfig"]["LogConfig"]["Type"], "none")
            self.assertTrue(container["HostConfig"]["ReadonlyRootfs"])
            self.assertEqual(container["Config"]["Image"], IMAGE)
            self.assertIn({"Name": "fsize", "Hard": max_bytes, "Soft": max_bytes}, container["HostConfig"]["Ulimits"])
            for batch in batches:
                send(endpoint, json.dumps(batch).encode())
        finally:
            session.stop()
        metadata = session.finalize("test-stopped")
        self.assertTrue(metadata["private_modes"])
        private_json(root / "result.json", {"passed": True, "capture": metadata})
        return session.directory / "logs.jsonl"

    def test_allowlists_preserve_types_ids_and_duplicates(self):
        batch = fixtures.poisoned()
        path = self.capture("allowlists", [batch, batch])
        data = path.read_bytes()
        self.assertNotIn(fixtures.SENTINEL.encode(), data)
        observed = records(path)
        self.assertEqual(len(observed), 8)
        fingerprints = [hashlib.sha256(json.dumps(log, sort_keys=True).encode()).hexdigest()
                        for _, _, log in observed]
        self.assertEqual(len(set(fingerprints)), 4)
        for resource, scope, log in observed:
            resource_attrs = attrs(resource["resource"]["attributes"])
            self.assertEqual(set(resource_attrs), {"service.name", "service.version", "fleetdiff.capture.profile", *fixtures.IDENTITY})
            self.assertEqual(resource_attrs["service.name"], {"stringValue": "claude-code"})
            self.assertEqual(resource_attrs["fleetdiff.capture.profile"], {"stringValue": "claude-code-logs/v1"})
            self.assertNotIn("schemaUrl", resource)
            self.assertNotIn("schemaUrl", scope)
            self.assertEqual(scope["scope"].get("attributes", []), [])
            self.assertEqual(scope["scope"]["name"], "fleetdiff.claude-code-capture")
            fields = attrs(log["attributes"])
            for key, item in fixtures.IDENTITY.items():
                self.assertEqual(fields[key], {"stringValue": item})
            self.assertNotIn("error", fields)
            self.assertNotIn("model", fields)
            self.assertNotIn("query_source", fields)
            self.assertFalse(log.get("severityText"))
            self.assertFalse(log.get("traceId"))
            self.assertFalse(log.get("spanId"))
            self.assertEqual(log["body"]["stringValue"], "claude_code." + fields["event.name"]["stringValue"])
            if fields["event.name"]["stringValue"] == "api_request":
                for key, number in {"input_tokens": 7, "output_tokens": 4, "cache_read_tokens": 11,
                                    "cache_creation_tokens": 5, "cost_usd_micros": 34}.items():
                    self.assertEqual(fields[key], {"intValue": str(number)})
                self.assertEqual(fields["cost_usd"], {"doubleValue": 0.00003435})
            else:
                self.assertNotIn("input_tokens", fields)
                self.assertNotIn("cost_usd", fields)
        self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
        self.assertEqual(stat.S_IMODE(path.parent.stat().st_mode), 0o700)

    def test_wrong_type_content_is_removed_not_coerced(self):
        path = self.capture("types", [fixtures.malformed()])
        self.assertNotIn(fixtures.SENTINEL.encode(), path.read_bytes())
        resource, _, log = records(path)[0]
        self.assertEqual(set(attrs(resource["resource"]["attributes"])), {"service.name", "fleetdiff.capture.profile", "identity.source"})
        self.assertEqual(attrs(log["attributes"]), {"event.name": {"stringValue": "api_request"},
                                                   "identity.source": {"stringValue": "unverified"}})

    def test_identity_regimes_and_malformed_owners_cannot_fall_back(self):
        cases = fixtures.unverified_identity_cases()
        path = self.capture("identity-regimes", [case[3] for case in cases])
        self.assertNotIn(fixtures.SENTINEL.encode(), path.read_bytes())
        expected = {sequence: (location, key) for sequence, location, key, _ in cases}
        self.assertEqual(len(records(path)), len(cases))
        for resource, _, log in records(path):
            fields = attrs(log["attributes"])
            sequence = int(fields["event.sequence"]["intValue"])
            location, malformed_key = expected[sequence]
            target = attrs(resource["resource"]["attributes"]) if location == "resource" else fields
            self.assertEqual(target["identity.source"], {"stringValue": "unverified"})
            if malformed_key != "identity.source":
                self.assertNotIn(malformed_key, target)
        if CLI:
            result = investigate(path)
            self.assertEqual(result.returncode, 0, result.stderr)
            report = json.loads(result.stdout)
            self.assertEqual(report["before"]["unattributed_records"], len(cases))
            private_json(evidence_root() / "cli-identity-regimes.json", report)

    def test_conflicting_identity_locality_is_not_reconciled(self):
        path = self.capture("conflict", [fixtures.conflict()])
        for resource, _, log in records(path):
            self.assertEqual(attrs(resource["resource"]["attributes"])["user.account_uuid"],
                             {"stringValue": "synthetic-conflicting-owner"})
            self.assertEqual(attrs(log["attributes"])["user.account_uuid"],
                             {"stringValue": "synthetic-account-a"})

    def test_unknown_events_and_bodies_are_dropped(self):
        batch = fixtures.poisoned()
        scope = batch["resourceLogs"][0]["scopeLogs"][0]
        scope["logRecords"] = scope["logRecords"][4:]
        path = self.capture("unknown", [batch])
        self.assertEqual(records(path), [])
        self.assertNotIn(fixtures.SENTINEL.encode(), path.read_bytes())

    def test_zero_negative_and_missing_remain_distinct(self):
        batch = fixtures.envelope([fixtures.record()])
        log = batch["resourceLogs"][0]["scopeLogs"][0]["logRecords"][0]
        log["attributes"] = fixtures.attributes({"event.name": "api_request", "input_tokens": 0,
                                                "output_tokens": -1, "cost_usd": 0})
        path = self.capture("missing", [batch])
        fields = attrs(records(path)[0][2]["attributes"])
        self.assertEqual(fields["input_tokens"], {"intValue": "0"})
        self.assertEqual(fields["output_tokens"], {"intValue": "-1"})
        self.assertNotIn("cache_read_tokens", fields)
        self.assertEqual(fields["cost_usd"], {"intValue": "0"})

    def test_hard_file_cap_fails_closed(self):
        session = Capture(evidence_root() / "size-cap", max_bytes=1024)
        try:
            endpoint = session.start()
            try:
                send(endpoint, json.dumps(fixtures.clean()).encode())
            except (urllib.error.URLError, ConnectionError, TimeoutError):
                pass
        finally:
            session.stop()
        self.assertLessEqual((session.directory / "logs.jsonl").stat().st_size, 1024)
        with self.assertRaises(RuntimeError):
            session.finalize("size-cap")

    def test_cli_timer_flushes_and_cleans_up(self):
        root = evidence_root() / "cli"
        process = subprocess.Popen([sys.executable, "-B", str(ROOT / "capture.py"), str(root), "--seconds", "2"],
                                   cwd=evidence_root(), stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        try:
            first = process.stdout.readline().strip()
            self.assertTrue(first.startswith("OTLP logs endpoint: http://127.0.0.1:"))
            send(first.split(": ", 1)[1], json.dumps(fixtures.clean()).encode())
            output, error = process.communicate(timeout=30)
            self.assertEqual(process.returncode, 0, error)
            self.assertNotIn(fixtures.SENTINEL, output + error)
            self.assertEqual(len(records(root / "logs.jsonl")), 4)
            self.assertEqual(json.loads((root / "capture.json").read_text())["stop_reason"], "timer")
        finally:
            if process.poll() is None:
                process.terminate()
                process.communicate(timeout=30)

    def test_cli_termination_flushes_and_cleans_up(self):
        root = evidence_root() / "signal"
        process = subprocess.Popen([sys.executable, "-B", str(ROOT / "capture.py"), str(root), "--seconds", "30"],
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        try:
            endpoint = process.stdout.readline().strip().split(": ", 1)[1]
            send(endpoint, json.dumps(fixtures.clean()).encode())
            process.terminate()
            _, error = process.communicate(timeout=30)
            self.assertEqual(process.returncode, 0, error)
            self.assertEqual(len(records(root / "logs.jsonl")), 4)
            self.assertEqual(json.loads((root / "capture.json").read_text())["stop_reason"], "signal")
        finally:
            if process.poll() is None:
                process.terminate()
                process.communicate(timeout=30)

    @unittest.skipUnless(CLI, "local-checkout CLI smoke check not enabled")
    def test_filtered_file_is_accepted_and_exact_copies_are_optional(self):
        batch = fixtures.poisoned()
        path = self.capture("cli-filtered", [batch, batch])
        for deduplicate, count in ((False, 2), (True, 1)):
            result = investigate(path, deduplicate=deduplicate)
            self.assertEqual(result.returncode, 0, result.stderr)
            report = json.loads(result.stdout)
            for period in ("before", "after"):
                self.assertEqual(report[period]["observed_api_request_records"], count)
                self.assertEqual(report[period]["inclusive_input"]["tokens"], 23 * count)
                self.assertEqual(report[period]["output"]["tokens"], 4 * count)
            self.assertEqual(report["finality"], "unknown")
            private_json(evidence_root() / ("cli-filtered-dedup-" + str(deduplicate) + ".json"), report)


if __name__ == "__main__":
    result = unittest.TextTestRunner(verbosity=2).run(unittest.defaultTestLoader.loadTestsFromModule(sys.modules[__name__]))
    if EVIDENCE is not None:
        private_json(EVIDENCE / "summary.json", {"tests": result.testsRun, "failures": len(result.failures),
                     "errors": len(result.errors), "skipped": len(result.skipped), "image": IMAGE,
                     "relay_image": RELAY_IMAGE,
                     "config_sha256": hashlib.sha256((ROOT / "collector.yaml").read_bytes()).hexdigest()})
        print("Private synthetic evidence:", EVIDENCE)
    sys.exit(not result.wasSuccessful())
