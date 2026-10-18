# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Opt-in pinned proxy -> loopback provider -> real PostgreSQL coverage gate.

FLEETDIFF_SPEND_DOCKER=1 python3 -B test_row_coverage_runtime.py -v
Optional FLEETDIFF_COVERAGE_OUTPUT saves fresh synthetic evidence to a new directory.
No source-only check or manually inserted spend/daily row counts as runtime evidence.
"""
from contextlib import contextmanager
import csv
import hashlib
import io
import json
import os
from pathlib import Path
import time
import unittest
import uuid

from test_spend import DOCKER, PINS, ROOT, assert_private, docker, psql, require, run

CASES = ("success", "failure", "stream", "cache_fill", "cache_hit", "collision_first",
         "collision_second", "absent_user", "cross_midnight", "after_midnight")
SPEND_QUERY = '''SELECT COALESCE(json_agg(t), '[]'::json) FROM (
    SELECT request_id, call_type, "user", "startTime", "endTime", status, cache_hit,
           prompt_tokens, completion_tokens, total_tokens,
           metadata->>'status' AS metadata_status
    FROM public."LiteLLM_SpendLogs" ORDER BY "startTime", request_id) t;'''
DAILY_QUERY = '''SELECT COALESCE(json_agg(t), '[]'::json) FROM (
    SELECT date, user_id, model, custom_llm_provider, endpoint,
           api_requests, successful_requests, failed_requests,
           prompt_tokens, completion_tokens
    FROM public."LiteLLM_DailyUserSpend" ORDER BY date, user_id, model) t;'''


def query(container, sql):
    return json.loads(psql(container, "-At", "-c", sql))


def totals(snapshot):
    return {"spend_rows": len(snapshot["spend"]), **{
        field: sum(row[field] for row in snapshot["daily"])
        for field in ("api_requests", "successful_requests", "failed_requests")}}


@contextmanager
def runtime(output, failure_logging="default"):
    name = "fleetdiff-coverage-" + uuid.uuid4().hex
    gateway, volume = name + "-proxy", name + "-data"
    native_mount = []
    if os.environ.get("FLEETDIFF_LINUX_BIN"):
        try:
            binary = Path(os.environ["FLEETDIFF_LINUX_BIN"]).resolve(strict=True)
            with binary.open("rb") as source:
                require(source.read(4) == b"\x7fELF" and os.access(binary, os.X_OK),
                        "native runtime parity requires an executable Linux ELF binary")
        except OSError:
            raise AssertionError("cannot read native runtime binary; details suppressed") from None
        native_mount = ["--mount", f"type=bind,src={binary},dst=/native/fleetdiff,readonly"]
    docker("volume", "create", volume)
    try:
        docker("run", "-d", "--name", name, "--pull=never", "--network=none", "--read-only",
               "--tmpfs", "/tmp:rw,noexec,nosuid,size=64m",
               "--tmpfs", "/var/run/postgresql:rw,noexec,nosuid,size=16m",
               "--mount", f"type=volume,src={volume},dst=/var/lib/postgresql/data",
               "--mount", f"type=bind,src={ROOT},dst=/example,readonly",
               "--pids-limit=128", "--memory=512m", "--log-driver=none",
               "-e", "POSTGRES_DB=fixture", "-e", "POSTGRES_HOST_AUTH_METHOD=trust",
               *native_mount, PINS["postgres"], "-c", "listen_addresses=127.0.0.1",
               "-c", "log_statement=none", "-c", "log_min_error_statement=panic")
        deadline = time.monotonic() + 60
        while True:
            process = run(["docker", "exec", name, "cat", "/proc/1/comm"])
            ready = run(["docker", "exec", name, "pg_isready", "-U", "postgres", "-d", "fixture"])
            if process.stdout.strip() == b"postgres" and ready.returncode == 0:
                break
            require(time.monotonic() < deadline, "runtime PostgreSQL startup timed out")
            time.sleep(0.25)
        require(PINS["postgres_version"].encode() in docker("exec", name, "postgres", "--version"),
                "PostgreSQL pin mismatch")
        common = ["--pull=never", "--network=container:" + name, "--read-only", "--cap-drop=ALL",
                  "--security-opt=no-new-privileges", "--tmpfs", "/tmp:rw,nosuid,size=512m",
                  "--memory=2g", "--pids-limit=256", "--log-driver=none",
                  "--mount", f"type=bind,src={ROOT},dst=/example,readonly",
                  "-e", "DATABASE_URL=postgresql://postgres@127.0.0.1:5432/fixture",
                  "-e", "HOME=/tmp", "-e", "PYTHONDONTWRITEBYTECODE=1", "-e", "TZ=UTC",
                  "-e", "LITELLM_LOCAL_MODEL_COST_MAP=True", "-e", "LITELLM_TELEMETRY=False",
                  "-e", "STORE_MODEL_IN_DB=False", "-e", "DISABLE_SCHEMA_UPDATE=true"]
        common.extend(("-e", "COVERAGE_FAILURE_LOGGING=" + failure_logging))
        # Full upstream schema, installed offline CLI and engines. No handwritten DB schema.
        result = run(["docker", "run", "--rm", *common, "--entrypoint", "prisma", PINS["litellm"],
                      "db", "push", "--schema", "/app/schema.prisma", "--skip-generate"], timeout=150)
        if output:
            (output / "schema-init.log").write_bytes(result.stdout + result.stderr)
        require(result.returncode == 0, "offline Prisma schema initialization failed")
        docker("run", "-d", "--name", gateway, *common, "--entrypoint", "python",
               PINS["litellm"], "-B", "/example/coverage_runtime_probe.py", "serve")
        deadline = time.monotonic() + 180
        while run(["docker", "exec", gateway, "/bin/sh", "-c", "test -f /tmp/coverage-ready"]).returncode:
            require(time.monotonic() < deadline, "runtime proxy startup timed out")
            state = json.loads(docker("inspect", gateway))[0]["State"]
            require(state["Running"], "runtime proxy exited before readiness")
            time.sleep(0.5)
        yield name, gateway
    finally:
        if output:
            result = run(["docker", "exec", gateway, "cat", "/tmp/coverage-proxy.log"])
            if result.returncode == 0:
                (output / "proxy.log").write_bytes(result.stdout)
        run(["docker", "rm", "-f", gateway])
        run(["docker", "rm", "-f", name])
        require(run(["docker", "volume", "rm", volume]).returncode == 0,
                "runtime named volume cleanup failed")


class RuntimeCoverageTests(unittest.TestCase):
    def check_native_export(self, database, spend_bytes, daily_bytes, records):
        from test_native_export import apply_documented_grants

        apply_documented_grants(database)
        directory = "/tmp/coverage-export-private/native"
        inspected = json.loads(docker("inspect", database))[0]
        require(inspected["HostConfig"]["NetworkMode"] == "none" and
                any(m["Destination"] == "/native/fleetdiff" and not m["RW"]
                    for m in inspected["Mounts"]),
                "native runtime export requires network-none and a read-only binary mount")
        identity = psql(database, "-At", "-c", '''SELECT current_user,
            has_column_privilege(current_user, 'public."LiteLLM_SpendLogs"', 'request_id', 'SELECT'),
            has_table_privilege(current_user, 'public."LiteLLM_SpendLogs"', 'INSERT,UPDATE,DELETE'),
            has_column_privilege(current_user, 'public."LiteLLM_DailyUserSpend"', 'api_requests', 'SELECT'),
            has_table_privilege(current_user, 'public."LiteLLM_DailyUserSpend"', 'INSERT,UPDATE,DELETE');''',
            reader=True)
        require(identity.strip() == b"fixture_reader|t|f|t|f", "native runtime reader privileges differ")
        result = run(["docker", "exec", database, "env", "-i",
            "PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/tmp", "LC_ALL=C",
            "FLEETDIFF_LITELLM_DATABASE_URL=postgres://fixture_reader@/fixture?host=/var/run/postgresql&sslmode=disable",
            "sh", "-c", 'umask 000; exec "$@"', "native-runtime",
            "/native/fleetdiff", "export", "litellm", "--out", directory,
            "--from", "2026-10-07T00:00:00Z", "--to", "2026-10-09T00:00:00Z",
            "--format", "csv", "--daily"])
        assert_private(result, records=records)
        require(result.returncode == 0, "native export of the full pinned Prisma database failed")
        require(docker("exec", database, "ls", "-A1", directory).splitlines() ==
                [b"daily.csv", b"spend.csv"], "native runtime export created unexpected files")
        require(docker("exec", database, "stat", "-c", "%a", directory).strip() == b"700",
                "native runtime output directory is not private")
        for name, expected in (("spend.csv", spend_bytes), ("daily.csv", daily_bytes)):
            path = directory + "/" + name
            require(docker("exec", database, "stat", "-c", "%a", path).strip() == b"600",
                    "native runtime output file is not private")
            require(docker("exec", database, "cat", path) == expected,
                    "native runtime output bytes differ from the paired psql oracle")
        return {"full_pinned_prisma_schema": True, "reader_role": "fixture_reader",
                "documented_column_grants_only": True,
                "spend_csv_byte_equivalent": True, "daily_csv_byte_equivalent": True}

    def export_with_helper(self, database, output):
        parent = "/tmp/coverage-export-private"
        directory = parent + "/paired"
        service_env = ["-e", "PGSERVICEFILE=/example/coverage_runtime_pg_service.conf"]
        reader = ["docker", "exec", *service_env, database, "psql", "service=coverage_fixture",
                  "-Xq", "-v", "ON_ERROR_STOP=1"]
        identity = run([*reader, "-At", "-c", '''SELECT current_user,
            current_setting('default_transaction_read_only'),
            has_table_privilege(current_user, 'public."LiteLLM_SpendLogs"', 'SELECT'),
            has_table_privilege(current_user, 'public."LiteLLM_SpendLogs"', 'INSERT'),
            has_table_privilege(current_user, 'public."LiteLLM_DailyUserSpend"', 'INSERT');'''])
        self.assertEqual(identity.returncode, 0)
        self.assertEqual(identity.stdout.strip(), b"fixture_reader|on|t|f|f")
        docker("exec", database, "mkdir", "-m", "700", parent)
        helper = ["docker", "exec", *service_env, database, "sh", "/example/export-coverage.sh"]
        bounds = ["2026-10-07T00:00:00Z", "2026-10-09T00:00:00Z"]
        completed = run([*helper, directory, *bounds, "coverage_fixture"])
        if output:
            (output / "helper.stdout").write_bytes(completed.stdout)
            (output / "helper.stderr").write_bytes(completed.stderr)
        self.assertEqual(completed.returncode, 0, "guarded export helper failed")
        self.assertFalse(completed.stderr)
        self.assertNotIn(directory.encode(), completed.stdout)
        names = ("spend.csv", "daily.csv", "private-psql.out", "private-psql.err")
        self.assertEqual(sorted(docker("exec", database, "ls", "-1", directory).decode().splitlines()),
                         sorted(names))
        modes = {"directory": docker("exec", database, "stat", "-c", "%a", directory).decode().strip()}
        files = {}
        inodes = []
        for name in names:
            path = directory + "/" + name
            files[name] = docker("exec", database, "cat", path)
            modes[name] = docker("exec", database, "stat", "-c", "%a", path).decode().strip()
            inodes.append(docker("exec", database, "stat", "-c", "%d:%i", path).decode().strip())
            if output and name.startswith("private-"):
                (output / name).write_bytes(files[name])
        self.assertEqual(modes, {"directory": "700", **dict.fromkeys(names, "600")})
        self.assertEqual(len(set(inodes)), len(names))
        self.assertFalse(files["private-psql.out"])
        self.assertFalse(files["private-psql.err"])

        # The previous overwrite exploit must fail before either output is opened.
        rejected = []
        literal = parent + "/alias-output.csv"
        for label, arguments in (
            ("path_alias", ["csv_file=" + literal, "daily_csv_file=" + parent + "/./alias-output.csv"]),
            ("literal_paths", ["csv_file=" + literal, "daily_csv_file=" + parent + "/other.csv"]),
            ("same_descriptor", ["csv_file=/dev/fd/3", "daily_csv_file=/dev/fd/3"]),
            ("same_descriptor_leading_zero", ["csv_file=/dev/fd/3", "daily_csv_file=/dev/fd/03"]),
            ("daily_jsonl", ["csv_file=/dev/fd/3", "daily_csv_file=/dev/fd/4",
                             "jsonl_file=" + literal]),
        ):
            command = [*reader, "-v", "start_utc=" + bounds[0], "-v", "end_utc=" + bounds[1]]
            for argument in arguments:
                command.extend(("-v", argument))
            result = run([*command, "-f", "/example/export.sql"])
            self.assertNotEqual(result.returncode, 0, label)
            self.assertFalse(result.stdout, label)
            rejected.append(label)
            if output:
                (output / (label + ".private.err")).write_bytes(result.stderr)
        self.assertEqual(docker("exec", database, "ls", "-1", parent).decode().splitlines(), ["paired"])

        alias = parent + "/directory-link"
        dangling = parent + "/dangling-link"
        docker("exec", database, "ln", "-s", directory, alias)
        docker("exec", database, "ln", "-s", parent + "/absent-target", dangling)
        for target in (directory, alias, dangling):
            result = run([*helper, target, *bounds, "coverage_fixture"])
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(result.stdout)
            self.assertNotIn(target.encode(), result.stderr)
            for name in names:
                self.assertEqual(docker("exec", database, "cat", directory + "/" + name), files[name])
        self.assertNotEqual(run(["docker", "exec", database, "stat", parent + "/absent-target"]).returncode, 0)

        # DB diagnostics are retained privately, never copied into the public message.
        failed_directory = parent + "/failed-service"
        sentinel = "coverage_missing_service_sentinel"
        result = run([*helper, failed_directory, *bounds, sentinel])
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(result.stdout)
        self.assertNotIn(sentinel.encode(), result.stderr)
        self.assertNotIn(failed_directory.encode(), result.stderr)
        diagnostic = docker("exec", database, "cat", failed_directory + "/private-psql.err")
        self.assertIn(sentinel.encode(), diagnostic)
        self.assertEqual(docker("exec", database, "stat", "-c", "%a",
                               failed_directory + "/private-psql.err").strip(), b"600")
        if output:
            (output / "failed-service.private.err").write_bytes(diagnostic)
        safety = {"reader_role": "fixture_reader", "default_transaction_read_only": True,
                  "file_modes": modes, "distinct_output_inodes": True,
                  "rejected_before_output": rejected, "existing_directory_refused": True,
                  "directory_symlink_refused": True, "dangling_symlink_refused": True,
                  "private_diagnostics": True}
        return files["spend.csv"], files["daily.csv"], safety

    @unittest.skipUnless(DOCKER, "set FLEETDIFF_SPEND_DOCKER=1 for the real pinned offline runtime")
    def test_real_proxy_persistence(self):
        self.run_cases(CASES, "default")

    @unittest.skipUnless(DOCKER, "set FLEETDIFF_SPEND_DOCKER=1 for the real pinned offline runtime")
    def test_failure_logging_enabled(self):
        self.run_cases(("success", "failure"), "enabled")

    @unittest.skipUnless(DOCKER, "set FLEETDIFF_SPEND_DOCKER=1 for the real pinned offline runtime")
    def test_failure_logging_disabled(self):
        self.run_cases(("success", "failure"), "disabled")

    def run_cases(self, cases, failure_logging):
        output = (Path(os.environ["FLEETDIFF_COVERAGE_OUTPUT"]) / failure_logging
                  if os.environ.get("FLEETDIFF_COVERAGE_OUTPUT") else None)
        if output:
            output.mkdir(mode=0o700, parents=True, exist_ok=False)
        evidence = {"images": PINS, "failure_logging": failure_logging,
                    "clock_control": "LiteLLM request timing only; no persistence patch",
                    "cases": []}
        with runtime(output, failure_logging) as (database, gateway):
            pins = json.loads(docker("exec", gateway, "python", "-B", "/example/coverage_runtime_probe.py", "pins"))
            self.assertEqual(pins["version"], PINS["litellm_version"])
            for kind in ("schema", "writer"):
                self.assertEqual(pins["source_sha256"][kind], PINS[kind + "_sha256"])
            evidence["installed"] = pins
            evidence["network"] = {container: json.loads(docker("inspect", container))[0]["HostConfig"]["NetworkMode"]
                                   for container in (database, gateway)}
            previous = {"spend_rows": 0, "api_requests": 0, "successful_requests": 0, "failed_requests": 0}
            for case in cases:
                result = json.loads(docker("exec", gateway, "python", "-B", "/example/coverage_runtime_probe.py",
                                           "exercise", case, timeout=90))
                self.assertEqual(result["http_status"], 500 if case == "failure" else 200, case)
                # Both real background writers must settle; no direct writer invocation.
                started = time.monotonic()
                deadline, stable_since, last = started + 75, None, None
                disabled_failure = case == "failure" and failure_logging == "disabled"
                expected = {"spend_rows": int(not disabled_failure and case != "collision_second"),
                            "api_requests": int(not disabled_failure),
                            "successful_requests": int(case != "failure"),
                            "failed_requests": int(case == "failure" and not disabled_failure)}
                while True:
                    snapshot = {"spend": query(database, SPEND_QUERY), "daily": query(database, DAILY_QUERY)}
                    if snapshot == last:
                        stable_since = stable_since or time.monotonic()
                    else:
                        stable_since = None
                    current = totals(snapshot)
                    delta = {key: current[key] - previous[key] for key in current}
                    if (stable_since and time.monotonic() - stable_since >= 8 and delta == expected
                            and (not disabled_failure or time.monotonic() - started >= 20)):
                        break
                    require(time.monotonic() < deadline, "runtime persistence did not settle")
                    last = snapshot
                    time.sleep(0.5)
                result["delta"] = {key: current[key] - previous[key] for key in current}
                self.assertEqual(result["delta"], expected, case)
                self.assertEqual(len(result["provider_calls"]), 0 if case == "cache_hit" else 1, case)
                if case == "stream":
                    self.assertTrue(result["stream_done"])
                result["snapshot"] = snapshot
                previous = current
                evidence["cases"].append(result)
                print(json.dumps({"profile": failure_logging, "case": case, "http": result["http_status"],
                                  "forwards": len(result["provider_calls"]), "delta": result["delta"]}), flush=True)
                if output:
                    (output / "observed.json").write_text(json.dumps(evidence, indent=2) + "\n")
            if failure_logging == "default":
                indexed = {entry["case"]: entry for entry in evidence["cases"]}
                self.assertEqual(indexed["collision_first"]["response_ids"],
                                 indexed["collision_second"]["response_ids"])
                self.assertEqual(indexed["cache_fill"]["response_ids"], indexed["cache_hit"]["response_ids"])
                rows = {row["request_id"]: row for row in snapshot["spend"]}
                self.assertEqual(rows["chatcmpl-coverage-absent_user"]["user"], "")
                self.assertTrue(any(row["user_id"] == "" and row["api_requests"] == 1
                                    for row in snapshot["daily"]))
                boundary = rows["chatcmpl-coverage-cross_midnight"]
                self.assertTrue(boundary["startTime"].startswith("2026-10-07T23:59:59"))
                self.assertTrue(boundary["endTime"].startswith("2026-10-08T00:00:01"))
                self.assertEqual(sum(row["api_requests"] for row in indexed["cross_midnight"]["snapshot"]["daily"]
                                     if row["date"] == "2026-10-08"), 0)
                self.assertEqual(sum(row["api_requests"] for row in snapshot["daily"]
                                     if row["date"] == "2026-10-08"), 1)
            # Independent SELECT of daily counters and actual repository SpendLogs export.
            psql(database, data=b'''CREATE ROLE fixture_reader LOGIN;
                GRANT USAGE ON SCHEMA public TO fixture_reader;
                GRANT SELECT ON public."LiteLLM_SpendLogs" TO fixture_reader;
                GRANT SELECT (date, api_requests, successful_requests, failed_requests)
                    ON public."LiteLLM_DailyUserSpend" TO fixture_reader;''')
            from test_spend import export_database
            exports = export_database(database)
            exported = list(csv.DictReader(io.StringIO(exports["csv"].decode())))
            evidence["export_rows"] = len(exported)
            evidence["export_rows_by_start_day"] = {day: sum(row["startTime"].startswith(day) for row in exported)
                for day in ("2026-10-07", "2026-10-08")}
            self.assertEqual(len(exported), len(snapshot["spend"]))
            spend_bytes, daily_bytes, safety = self.export_with_helper(database, output)
            evidence["export_safety"] = safety
            if os.environ.get("FLEETDIFF_LINUX_BIN"):
                evidence["native_export"] = self.check_native_export(
                    database, spend_bytes, daily_bytes, exported)
            daily_export = list(csv.DictReader(io.StringIO(daily_bytes.decode())))
            exported_again = list(csv.DictReader(io.StringIO(spend_bytes.decode())))
            self.assertEqual(sorted(exported, key=lambda r: r["request_id"]),
                             sorted(exported_again, key=lambda r: r["request_id"]))
            independently_summed = {}
            for row in snapshot["daily"]:
                counters = independently_summed.setdefault(row["date"], dict.fromkeys(
                    ("api_requests", "successful_requests", "failed_requests"), 0))
                for field in counters:
                    counters[field] += row[field]
            self.assertEqual({row["date"]: {field: int(row[field]) for field in
                ("api_requests", "successful_requests", "failed_requests")} for row in daily_export},
                independently_summed)
            evidence["daily_export"] = daily_export
            evidence["export_source_sha256"] = {name: hashlib.sha256((ROOT / name).read_bytes()).hexdigest()
                for name in ("export.sql", "daily-projection.sql", "export-coverage.sh")}
            evidence["harness_sha256"] = {name: hashlib.sha256((ROOT / name).read_bytes()).hexdigest()
                for name in ("coverage_runtime_probe.py", "test_row_coverage_runtime.py",
                             "coverage_runtime_pg_service.conf", "test_native_export.py")}
            if output:
                for fmt, data in exports.items():
                    (output / ("spend." + fmt)).write_bytes(data)
                (output / "daily.csv").write_bytes(daily_bytes)
                (output / "spend-with-daily.csv").write_bytes(spend_bytes)
                (output / "observed.json").write_text(json.dumps(evidence, indent=2) + "\n")


if __name__ == "__main__":
    unittest.main()
