# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Pinned dropped-row fixture through the built local CLI; no runtime required."""
import csv
import json
import os
from pathlib import Path
import tempfile
import unittest

import coverage_runtime_fixture
from test_spend import CLI as BIN, ROOT, SECRET, require, run


class CoverageExportSafetyTests(unittest.TestCase):
    def test_fresh_private_outputs_and_existing_aliases(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            commands = root / "commands"
            commands.mkdir()
            client = commands / "psql"
            client.write_text("#!/bin/sh\nset -eu\nprintf 'request_id,call_type\\n' >&3\n"
                              "printf 'date,api_requests,successful_requests,failed_requests\\n' >&4\n")
            client.chmod(0o700)
            env = dict(os.environ, PATH=str(commands) + ":/usr/bin:/bin")
            output = root / "PRIVATE output"
            command = ["sh", str(ROOT / "export-coverage.sh"), str(output),
                       "2026-10-07T00:00:00Z", "2026-10-09T00:00:00Z"]
            result = run(command, env=env)
            require(result.returncode == 0, "guarded export failed")
            require(str(output).encode() not in result.stdout + result.stderr, "export path escaped")
            self.assertEqual(output.stat().st_mode & 0o777, 0o700)
            expected = {name: (output / name).read_bytes() for name in
                        ("spend.csv", "daily.csv", "private-psql.out", "private-psql.err")}
            self.assertNotEqual(expected["spend.csv"], expected["daily.csv"])
            for name in expected:
                self.assertEqual((output / name).stat().st_mode & 0o777, 0o600)
            alias = root / "PRIVATE_alias"
            alias.symlink_to(output, target_is_directory=True)
            for destination in (str(output), str(output) + "/.", str(alias)):
                retry = command.copy()
                retry[2] = destination
                result = run(retry, env=env)
                require(result.returncode != 0 and not result.stdout,
                        "existing or aliased output directory accepted")
                require(destination.encode() not in result.stderr, "error exposed output path")
                self.assertEqual(expected, {name: (output / name).read_bytes() for name in expected})

    def test_client_diagnostics_stay_private(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            commands = root / "commands"
            commands.mkdir()
            client = commands / "psql"
            client.write_text("#!/bin/sh\nprintf 'PRIVATE_CONNECTION_SENTINEL\\n' >&2\nexit 1\n")
            client.chmod(0o700)
            output = root / "PRIVATE_output"
            result = run(["sh", str(ROOT / "export-coverage.sh"), str(output),
                          "2026-10-07T00:00:00Z", "2026-10-09T00:00:00Z"],
                         env=dict(os.environ, PATH=str(commands) + ":/usr/bin:/bin"))
            require(result.returncode != 0 and not result.stdout, "client failure ignored")
            require(b"PRIVATE_CONNECTION_SENTINEL" not in result.stderr, "client diagnostics escaped")
            self.assertIn(b"PRIVATE_CONNECTION_SENTINEL", (output / "private-psql.err").read_bytes())


class RowCoverageTests(unittest.TestCase):
    def test_recorded_fixture(self):
        coverage_runtime_fixture.check()
        expected = json.loads((ROOT / "coverage_runtime_expected.json").read_text())
        for name, digest in expected["provenance"]["export_source_sha256"].items():
            self.assertEqual(coverage_runtime_fixture.digest(ROOT / name), digest)
        for name, digest in expected["provenance"]["runtime_harness_sha256"].items():
            self.assertEqual(coverage_runtime_fixture.digest(ROOT / name), digest)

    @unittest.skipUnless(BIN, "set FLEETDIFF_BIN for built CLI tests")
    def test_collision_fixture_and_safe_output(self):
        expected = json.loads((ROOT / "coverage_runtime_expected.json").read_text())
        path = ROOT / "coverage_runtime_spend.csv"
        daily = ROOT / "coverage_runtime_daily.csv"
        with path.open(newline="") as source:
            rows = list(csv.DictReader(source))
        base = [str(BIN), "investigate", "--litellm-spend", str(path),
                "--hash-secret-env", "FLEETDIFF_TEST_SECRET"]
        env = dict(os.environ, FLEETDIFF_TEST_SECRET=SECRET)
        for mode in ("comparison", "session"):
            mode_args = (["--before-period", "2026-10-07", "--after-period", "2026-10-08"]
                         if mode == "comparison" else ["--period", "2026-10-07"])
            for fmt in ("text", "json"):
                command = base + mode_args + ["--format", fmt]
                result = run(command + ["--litellm-daily-spend", str(daily)], env=env)
                require(result.returncode == 0 and not result.stderr, "coverage CLI failed")
                for row in rows:
                    for field in ("request_id", "api_key", "user", "team_id", "session_id", "agent_id", "model_id", "model"):
                        if row[field]:
                            require(row[field].encode() not in result.stdout + result.stderr,
                                    "coverage CLI leaked a raw fixture identifier")
                require(str(daily).encode() not in result.stdout + result.stderr,
                        "coverage CLI leaked an input path")
                if fmt == "text":
                    require(b"2026-10-07 UTC: 8 exported rows; 9 daily requests (8 successful, 1 failed); 1 missing row." in result.stdout,
                            "missing-row result changed")
                    require(result.stdout.startswith(b"Spend-log row coverage"),
                            "coverage was hidden below the usage headline")
                else:
                    report = json.loads(result.stdout)
                    for day in report["row_coverage"]["days"]:
                        want = expected["expected"]["days"][day["date_utc"]]
                        require(day["exported_rows"] == want["spend_rows"], "wrong exported count")
                        for key in ("api_requests", "successful_requests", "failed_requests"):
                            require(day[key] == want[key], "wrong daily count")
                        require(day["missing_rows"] == want["api_requests"] - want["spend_rows"],
                                "wrong missing-row difference")
                    plain = run(command, env=env)
                    require(plain.returncode == 0, "baseline comparison failed")
                    baseline = json.loads(plain.stdout)
                    report.pop("row_coverage")
                    require(report == baseline, "coverage adjusted accounting or identities")

    @unittest.skipUnless(BIN, "set FLEETDIFF_BIN for built CLI tests")
    def test_missing_reference_is_unknown(self):
        with tempfile.TemporaryDirectory() as directory:
            daily = Path(directory) / "PRIVATE_daily.csv"
            daily.write_text("date,api_requests,successful_requests,failed_requests\n")
            result = run([str(BIN), "investigate", "--litellm-spend", str(ROOT / "coverage_runtime_spend.csv"),
                          "--before-period", "2026-10-07", "--after-period", "2026-10-08",
                          "--litellm-daily-spend", str(daily), "--format", "json"])
            require(result.returncode == 0, "unknown coverage failed")
            for day in json.loads(result.stdout)["row_coverage"]["days"]:
                require(day["api_requests"] is None and day["missing_rows"] is None
                        and day["status"] == "daily_counts_missing", "missing reference became zero")


if __name__ == "__main__":
    unittest.main()
