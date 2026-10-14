# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Pinned rotation/retention checks using synthetic data and cached images only."""
import copy
from datetime import datetime, timedelta, timezone
import hashlib
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import time
import unittest
from unittest import mock

import capture
import fixtures
from test_capture import attrs, evidence_root, private_json, records

ROOT = capture.ROOT
OVERLAY = ROOT / "collector-long-running.json"
DOCKER = os.environ.get("FLEETDIFF_CLAUDE_CAPTURE_DOCKER") == "1"
CLI = os.environ.get("FLEETDIFF_CLAUDE_CAPTURE_CLI") == "1"


def policy():
    return json.loads(OVERLAY.read_text(encoding="ascii"))


def overlay_docker(*args):
    # Reuse the bounded helper's isolation/relay without changing its behavior.
    args = list(args)
    if args[0] == "create" and "--config=/etc/capture.yaml" in args:
        index = args.index(capture.IMAGE)
        args[index:index] = ["--mount", f"type=bind,src={OVERLAY},dst=/etc/rotation.json,readonly",
                             "--env", "CLAUDE_CAPTURE_DIR=/capture"]
        args.append("--config=/etc/rotation.json")
    return REAL_DOCKER(*args)


REAL_DOCKER = capture.docker


def import_files(paths, *, deduplicate=False):
    binary = os.environ.get("FLEETDIFF_BIN")
    if not binary:
        raise ValueError("rotation CLI checks require FLEETDIFF_BIN")
    command = [str(Path(binary).resolve()), "investigate"]
    for path in paths:
        command += ["--claude-code-logs", str(path)]
    command += ["--group-by", "user", "--format", "json"]
    if deduplicate:
        command.append("--deduplicate-exact")
    return subprocess.run(command, capture_output=True, text=True, timeout=30)


def write_batch(path, batch):
    with path.open("x", encoding="ascii") as output:
        os.fchmod(output.fileno(), 0o600)
        json.dump(batch, output, separators=(",", ":"))
        output.write("\n")


class PolicyTests(unittest.TestCase):
    def test_overlay_reuses_redaction_and_only_changes_route_and_exporter(self):
        value = policy()
        self.assertEqual(set(value), {"receivers", "exporters"})
        self.assertEqual(value["receivers"]["otlp"]["protocols"]["http"],
                         {"endpoint": "127.0.0.1:4318"})
        exporter = value["exporters"]["file/capture"]
        self.assertEqual(exporter["rotation"], {"max_megabytes": 8, "max_days": 15,
                                                "max_backups": 15, "localtime": False})
        self.assertFalse(exporter["append"])
        self.assertFalse(exporter["create_directory"])
        self.assertNotIn("group_by", exporter)
        self.assertNotIn("compression", exporter)
        self.assertEqual(exporter["path"], "${env:CLAUDE_CAPTURE_DIR}/logs.jsonl")


@unittest.skipUnless(DOCKER, "set FLEETDIFF_CLAUDE_CAPTURE_DOCKER=1; cached images required")
class RotationTests(unittest.TestCase):
    def validate(self, overlay):
        return subprocess.run([
            "docker", "run", "--rm", "--pull", "never", "--network", "none",
            "--user", f"{os.getuid()}:{os.getgid()}",
            "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges",
            "--env", "CLAUDE_CAPTURE_DIR=/capture",
            "--mount", f"type=bind,src={ROOT / 'collector.yaml'},dst=/etc/base.yaml,readonly",
            "--mount", f"type=bind,src={overlay},dst=/etc/rotation.json,readonly",
            capture.IMAGE, "validate", "--config=/etc/base.yaml", "--config=/etc/rotation.json",
        ], capture_output=True, text=True, timeout=30)

    def test_pinned_binary_accepts_composition_and_rejects_append_rotation(self):
        capture.docker("image", "inspect", capture.IMAGE)
        valid = self.validate(OVERLAY)
        self.assertEqual(valid.returncode, 0, valid.stderr)
        invalid = policy()
        invalid["exporters"]["file/capture"]["append"] = True
        path = evidence_root() / "invalid-append.json"
        private_json(path, invalid)
        rejected = self.validate(path)
        self.assertNotEqual(rejected.returncode, 0)
        self.assertIn("append and rotation", rejected.stderr)
        private_json(evidence_root() / "validation.json", {
            "valid_exit": valid.returncode, "append_rotation_exit": rejected.returncode,
            "image": capture.IMAGE,
        })

    def test_exact_policy_rotation_age_count_privacy_and_modes(self):
        root = evidence_root() / "rotation"
        root.mkdir(mode=0o700)
        session = capture.Capture(root / "capture", max_bytes=32 * 1024 * 1024)
        count, initial = 0, set()
        now = datetime.now(timezone.utc)
        old_name = ""
        try:
            with mock.patch("capture.docker", side_effect=overlay_docker):
                endpoint = session.start()
            self.assertTrue(json.loads(capture.docker("network", "inspect", session.name))[0]["Internal"])
            self.assertFalse(json.loads(capture.docker("inspect", session.name))[0]["HostConfig"]["PortBindings"])
            # Empty synthetic backups exercise the actual filename-age policy,
            # not a fake clock or a 15-day wall-clock soak.
            for i in range(2):
                timestamp = now - (timedelta(days=16) if i == 1 else timedelta(minutes=1))
                name = "logs-" + timestamp.strftime("%Y-%m-%dT%H-%M-%S.%f")[:-3] + "-size.jsonl"
                with (session.directory / name).open("xb") as output:
                    os.fchmod(output.fileno(), 0o600)
                initial.add(name)
                if i == 1:
                    old_name = name
            unrelated = session.directory / "unrelated.keep"
            with unrelated.open("xb") as output:
                os.fchmod(output.fileno(), 0o600)

            batch = fixtures.poisoned()
            batch["resourceLogs"][0]["scopeLogs"][0]["logRecords"] *= 64
            payload = json.dumps(batch).encode()
            self.assertLess(len(payload), capture.MAX_REQUEST)
            expected_per_batch = 4 * 64
            capture.send(endpoint, payload)
            count += 1
            deadline = time.monotonic() + 5
            while (session.directory / old_name).exists():
                if time.monotonic() >= deadline:
                    self.fail("age cleanup below the backup-count limit did not settle")
                time.sleep(0.05)
            self.assertTrue(all((session.directory / name).exists()
                                for name in initial if name != old_name))
            # Now independently exceed max_backups with recent empty files.
            for i in range(17):
                timestamp = now - timedelta(minutes=i + 2)
                name = "logs-" + timestamp.strftime("%Y-%m-%dT%H-%M-%S.%f")[:-3] + "-size.jsonl"
                with (session.directory / name).open("xb") as output:
                    os.fchmod(output.fileno(), 0o600)
                initial.add(name)
            for _ in range(256):
                capture.send(endpoint, payload)
                count += 1
                current = {path.name for path in session.directory.glob("logs-*.jsonl")}
                if len(current - initial) >= 2:
                    break
            else:
                self.fail("bounded synthetic workload did not trigger two rotations")

            deadline = time.monotonic() + 5
            while (len(list(session.directory.glob("logs-*.jsonl"))) > 15
                   or (session.directory / old_name).exists()):
                if time.monotonic() >= deadline:
                    self.fail("asynchronous retention cleanup did not settle")
                time.sleep(0.05)
        finally:
            session.stop()

        backups = sorted(session.directory.glob("logs-*.jsonl"))
        self.assertEqual(len(backups), 15)
        self.assertFalse((session.directory / old_name).exists())
        self.assertTrue(unrelated.exists(), "exporter must not delete unrelated paths")
        self.assertGreaterEqual(len({path.name for path in backups} - initial), 2)
        outputs = [*backups, session.directory / "logs.jsonl"]
        observed, modes, sizes = 0, {}, {}
        for path in outputs:
            data = path.read_bytes()
            self.assertNotIn(fixtures.SENTINEL.encode(), data)
            sizes[path.name] = len(data)
            self.assertLessEqual(len(data), 8 * 1024 * 1024)
            modes[path.name] = oct(stat.S_IMODE(path.stat().st_mode))
            for resource, scope, log in records(path):
                observed += 1
                self.assertNotIn("schemaUrl", resource)
                self.assertNotIn("schemaUrl", scope)
                self.assertEqual(scope["scope"].get("attributes", []), [])
                self.assertEqual(attrs(resource["resource"]["attributes"])["fleetdiff.capture.profile"],
                                 {"stringValue": "claude-code-logs/v1"})
                self.assertNotIn("model", attrs(log["attributes"]))
                self.assertNotIn("error", attrs(log["attributes"]))
        self.assertEqual(observed, count * expected_per_batch)
        self.assertEqual(stat.S_IMODE(session.directory.stat().st_mode), 0o700)
        # This pin forces 0644 on new rotated files. Do not certify a 0600 policy.
        self.assertEqual(modes["logs.jsonl"], "0o644")
        self.assertIn("0o644", [modes[path.name] for path in backups])
        private_json(root / "result.json", {
            "passed": True, "batches_sent": count, "records": observed,
            "backups_after_cleanup": len(backups), "age_seed_removed": old_name,
            "age_check_seed_count": 2, "recent_seed_retained_during_age_check": True,
            "unrelated_file_retained": True, "directory_mode": "0o700",
            "file_modes": modes, "file_bytes": sizes, "strict_0600_satisfied": False,
            "active_file_age_ttl": "not provided by exporter",
            "source_finality": "unknown", "image": capture.IMAGE,
        })
        if CLI:
            paths = [path for path in outputs if path.stat().st_size]
            for deduplicate in (False, True):
                result = import_files(paths, deduplicate=deduplicate)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertNotIn(fixtures.SENTINEL, result.stdout + result.stderr)
                report = json.loads(result.stdout)
                self.assertEqual(report["input_files"], len(paths))
                self.assertEqual(report["period_selection"], "observed_span_halves")
                self.assertEqual(report["finality"], "unknown")
                self.assertFalse(report["models"])
                expected = 1 if deduplicate else count * 64
                for period in ("before", "after"):
                    self.assertEqual(report[period]["observed_api_request_records"], expected)
                    self.assertEqual(report[period]["inclusive_input"]["tokens"], expected * 23)
                    self.assertEqual(report[period]["output"]["tokens"], expected * 4)
                private_json(root / f"cli-dedup-{deduplicate}.json", report)


@unittest.skipUnless(CLI, "set FLEETDIFF_CLAUDE_CAPTURE_CLI=1 and FLEETDIFF_BIN")
class DefaultPeriodTests(unittest.TestCase):
    def test_repeated_files_retained_or_exact_filtered_and_both_defaults(self):
        root = evidence_root() / "defaults"
        root.mkdir(mode=0o700)
        paths = [root / "first.jsonl", root / "copied.jsonl"]
        for path in paths:
            write_batch(path, fixtures.clean())
        for deduplicate in (False, True):
            result = import_files(paths, deduplicate=deduplicate)
            self.assertEqual(result.returncode, 0, result.stderr)
            report = json.loads(result.stdout)
            self.assertEqual(report["input_files"], 2)
            self.assertEqual(report["period_selection"], "observed_span_halves")
            self.assertEqual(report["finality"], "unknown")
            for period in ("before", "after"):
                self.assertEqual(report[period]["observed_api_request_records"], 1 if deduplicate else 2)
            private_json(root / f"halves-dedup-{deduplicate}.json", report)

        weekly_paths = []
        dates = ("2026-09-20", "2026-09-23", "2026-10-02", "2026-10-05")
        for i in range(2):
            batch = fixtures.clean()
            scope = batch["resourceLogs"][0]["scopeLogs"][0]
            request = scope["logRecords"][0]
            scope["logRecords"] = []
            for date in dates[i * 2:i * 2 + 2]:
                log = copy.deepcopy(request)
                stamp = datetime.fromisoformat(date).replace(tzinfo=timezone.utc)
                log["timeUnixNano"] = str(int(stamp.timestamp()) * 1_000_000_000)
                scope["logRecords"].append(log)
            path = root / f"weeks-{i}.jsonl"
            write_batch(path, batch)
            weekly_paths.append(path)
        result = import_files(weekly_paths)
        self.assertEqual(result.returncode, 0, result.stderr)
        report = json.loads(result.stdout)
        self.assertEqual(report["input_files"], 2)
        self.assertEqual(report["period_selection"], "complete_utc_weeks")
        self.assertEqual(report["outside_periods"], 2)
        for period in ("before", "after"):
            self.assertEqual(report[period]["observed_api_request_records"], 1)
        private_json(root / "complete-weeks.json", report)


if __name__ == "__main__":
    os.umask(0o077)
    suite = unittest.defaultTestLoader.loadTestsFromModule(sys.modules[__name__])
    result = unittest.TextTestRunner(verbosity=2).run(suite)
    private_json(evidence_root() / "rotation-summary.json", {
        "tests": result.testsRun, "failures": len(result.failures),
        "errors": len(result.errors), "skipped": len(result.skipped),
        "image": capture.IMAGE, "relay_image": capture.RELAY_IMAGE,
        "base_config_sha256": hashlib.sha256((ROOT / "collector.yaml").read_bytes()).hexdigest(),
        "overlay_sha256": hashlib.sha256(OVERLAY.read_bytes()).hexdigest(),
        "cli_binary": os.environ.get("FLEETDIFF_BIN") if CLI else None,
    })
    sys.exit(not result.wasSuccessful())
