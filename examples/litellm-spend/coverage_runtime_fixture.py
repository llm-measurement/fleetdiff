# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Publish/check the small synthetic exports from an accepted offline runtime run.

python3 -B coverage_runtime_fixture.py --record RUN_DIR
python3 -B coverage_runtime_fixture.py
Only the allowlisted exports and compact provenance are published, not proxy logs.
"""
import argparse
import csv
import hashlib
import json
from pathlib import Path
import shutil

ROOT = Path(__file__).resolve().parent
COUNTERS = ("api_requests", "successful_requests", "failed_requests")


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def read_csv(path):
    with path.open(newline="") as handle:
        return list(csv.DictReader(handle))


def record(source):
    observed = json.loads((source / "default/observed.json").read_text())
    assert observed["export_rows"] == 9
    indexed = {case["case"]: case for case in observed["cases"]}
    first, second = indexed["collision_first"], indexed["collision_second"]
    assert first["response_ids"] == second["response_ids"] == ["chatcmpl-coverage-collision"]
    assert len(first["provider_calls"]) == len(second["provider_calls"]) == 1
    assert first["delta"]["spend_rows"] == 1 and second["delta"]["spend_rows"] == 0
    assert first["delta"]["api_requests"] == second["delta"]["api_requests"] == 1
    failures = {}
    for profile in ("default", "enabled", "disabled"):
        run = json.loads((source / profile / "observed.json").read_text())
        assert run["export_safety"] == observed["export_safety"]
        case = next(case for case in run["cases"] if case["case"] == "failure")
        failures[profile] = {"http_status": case["http_status"],
                             "provider_forwards": len(case["provider_calls"]), "delta": case["delta"]}
    files = {"spend": "coverage_runtime_spend.csv", "daily": "coverage_runtime_daily.csv"}
    shutil.copyfile(source / "default/spend-with-daily.csv", ROOT / files["spend"])
    shutil.copyfile(source / "default/daily.csv", ROOT / files["daily"])
    expected = {
        "license": "Apache-2.0", "synthetic": True,
        "provenance": {
            "images": observed["images"], "installed": observed["installed"],
            "export_source_sha256": observed["export_source_sha256"],
            "runtime_harness_sha256": observed["harness_sha256"],
            "fixture_generator_sha256": digest(Path(__file__)),
            "export_helper": "export-coverage.sh; one psql invocation with inherited distinct file descriptors",
            "export_safety": observed["export_safety"],
            "isolation": "PostgreSQL network=none; proxy, provider and client share its loopback namespace; no published ports",
            "clock_control": observed["clock_control"],
            "persistence": "Unmodified full image schema, proxy callbacks and asynchronous DB writers; no inserted spend or daily rows",
            "failure_logging": "default (disable_error_logs omitted)",
        },
        "files": {kind: {"path": name, "sha256": digest(ROOT / name)} for kind, name in files.items()},
        "cases": [{"case": case["case"], "http_status": case["http_status"],
                   "provider_forwards": len(case["provider_calls"]), "delta": case["delta"]}
                  for case in observed["cases"]],
        "expected": {
            "proxy_requests": 10, "provider_forwards": 9, "spend_rows": 9,
            "api_requests": 10, "successful_requests": 9, "failed_requests": 1,
            "days": {"2026-10-07": {"spend_rows": 8, "api_requests": 9, "successful_requests": 8,
                                     "failed_requests": 1},
                     "2026-10-08": {"spend_rows": 1, "api_requests": 1, "successful_requests": 1,
                                     "failed_requests": 0}},
            "collision": {"upstream_response_id": "chatcmpl-coverage-collision", "provider_forwards": 2,
                          "spend_rows": 1, "api_requests": 2},
            "cross_midnight": {"request_id": "chatcmpl-coverage-cross_midnight",
                               "startTime": "2026-10-07T23:59:59.000Z", "endTime": "2026-10-08T00:00:01.000Z",
                               "daily_date": "2026-10-07"},
            "failure_logging_profiles": failures,
        },
        "limits": ["One pinned version and synthetic chat-completions profile, retries disabled",
                   "Daily counters are spend-transaction counts, not universal ingress or provider-call counts",
                   "Identity-free daily sums require the same application and operation population as the export",
                   "Identical missing records on both sides, delayed writes, retention and configuration differences remain distinct limitations",
                   "Only the request clock is controlled; real transport latency is not measured"],
    }
    (ROOT / "coverage_runtime_expected.json").write_text(json.dumps(expected, indent=2) + "\n")


def check():
    fixture = json.loads((ROOT / "coverage_runtime_expected.json").read_text())
    for item in fixture["files"].values():
        assert digest(ROOT / item["path"]) == item["sha256"]
    spend = read_csv(ROOT / fixture["files"]["spend"]["path"])
    daily = read_csv(ROOT / fixture["files"]["daily"]["path"])
    assert tuple(daily[0]) == ("date", *COUNTERS)
    assert len(spend) == fixture["expected"]["spend_rows"]
    for day, counts in fixture["expected"]["days"].items():
        assert sum(row["startTime"].startswith(day) for row in spend) == counts["spend_rows"]
        row = next(row for row in daily if row["date"] == day)
        for field in COUNTERS:
            assert int(row[field]) == counts[field]
    for field in COUNTERS:
        assert sum(int(row[field]) for row in daily) == fixture["expected"][field]
    collision = fixture["expected"]["collision"]
    assert sum(row["request_id"] == collision["upstream_response_id"] for row in spend) == 1
    crossing = fixture["expected"]["cross_midnight"]
    row = next(row for row in spend if row["request_id"] == crossing["request_id"])
    assert row["startTime"] == crossing["startTime"] and row["endTime"] == crossing["endTime"]
    assert next(row for row in spend if row["request_id"] == "chatcmpl-coverage-absent_user")["user"] == ""
    print("PASS: 9 persisted SpendLogs rows vs 10 daily API requests; real two-forward response-ID collision; start-UTC day buckets")


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--record", type=Path)
    args = parser.parse_args()
    if args.record:
        record(args.record)
    check()
