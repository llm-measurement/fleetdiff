# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Fixture checks, optional CLI privacy tests, and actual pinned writer -> DB tests."""
from contextlib import contextmanager
import csv
from datetime import datetime, timezone
from decimal import Decimal
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock
import uuid

from generate import COLUMNS, ROOT, TOKEN_COLUMNS, rows, write_rows


PINS = json.loads((ROOT / "images.json").read_text())
CLI = os.environ.get("FLEETDIFF_BIN")
DOCKER = os.environ.get("FLEETDIFF_SPEND_DOCKER") == "1"
SECRET = "fleetdiff-synthetic-only-fixed-secret-20261010"
FORMATS = ("csv", "json", "jsonl")
GROUPS = ("key", "team", "user", "end-user", "session")
OPERATIONS = {"completion", "acompletion", "text_completion", "atext_completion",
              "anthropic_messages", "aanthropic_messages", "responses", "aresponses"}


def require(condition, message):
    # Never include raw rows, paths, command arguments, stdout or stderr in errors.
    if not condition:
        raise AssertionError(message)


def run(command, *, data=None, timeout=90, env=None):
    try:
        return subprocess.run(command, input=data, capture_output=True,
                              timeout=timeout, env=env)
    except (OSError, subprocess.SubprocessError):
        raise AssertionError("test subprocess could not complete; details suppressed") from None


def docker(*args, data=None, timeout=90):
    result = run(["docker", *map(str, args)], data=data, timeout=timeout)
    require(result.returncode == 0, "Docker test step failed; details suppressed")
    return result.stdout


def decode(data, encoding):
    text = data.decode("utf-8")
    if encoding == "csv":
        records = list(csv.DictReader(io.StringIO(text)))
    elif encoding == "json":
        records = json.loads(text)
    else:
        records = [json.loads(line) for line in text.splitlines() if line]
    for row in records:
        for key in TOKEN_COLUMNS:
            if row.get(key) not in (None, ""):
                row[key] = int(row[key])
        if row.get("spend") not in (None, ""):
            row["spend"] = str(Decimal(str(row["spend"])).normalize())
        for key in COLUMNS:
            if row.get(key) is None:
                row[key] = ""
    return records


def assert_private(result, paths=(), records=()):
    combined = result.stdout + result.stderr
    sentinels = ["SENTINEL_", "synthetic-", SECRET]
    sentinels.extend(str(path) for path in paths)
    for row in records:
        for key in ("request_id", "api_key", "user", "team_id", "organization_id",
                    "end_user", "model", "model_id", "model_group", "session_id"):
            if isinstance(row.get(key), str) and row[key]:
                sentinels.append(row[key])
    for value in sentinels:
        require(value.encode() not in combined, "output privacy sentinel detected")
        require(json.dumps(value)[1:-1].encode() not in combined,
                "JSON-escaped output privacy sentinel detected")


def investigate(path, *, group="key", encoding="json", hashes=False,
                records=(), success=True):
    require(bool(CLI), "set FLEETDIFF_BIN to the source-built CLI")
    command = [CLI, "investigate", "--litellm-spend", str(path),
               "--before-period", "2026-10-07", "--after-period", "2026-10-08",
               "--group-by", group, "--hash-secret-env", "FLEETDIFF_TEST_SECRET",
               "--format", encoding]
    if hashes:
        command.append("--show-hashes")
    environment = dict(os.environ, FLEETDIFF_TEST_SECRET=SECRET)
    result = run(command, env=environment)
    assert_private(result, (path, path.parent), records)
    require((result.returncode == 0) == success, "unexpected importer exit status")
    if success and encoding == "json":
        try:
            report = json.loads(result.stdout)
        except (ValueError, UnicodeError):
            raise AssertionError("importer did not return valid JSON") from None
        require(isinstance(report, dict) and bool(report), "empty importer report")
        return report
    return result


def assert_arithmetic(report, records):
    require(report["input_rows"] == len(records), "report source row count differs")
    require(report["out_of_scope_rows"] == sum(r["call_type"] not in OPERATIONS for r in records),
            "report excluded operation count differs")
    for name, day in (("before", 7), ("after", 8)):
        selected = [r for r in records if r["call_type"] in OPERATIONS and
                    r["startTime"].startswith(f"2026-10-{day:02}")]
        window = report[name]
        require(window["logged_model_requests"] == len(selected), "report request arithmetic differs")
        for field, metric in (("prompt_tokens", "input_tokens"),
                              ("completion_tokens", "output_tokens"),
                              ("cache_read_input_tokens", "cache_read_input_tokens"),
                              ("cache_write_input_tokens", "cache_write_input_tokens"),
                              ("reasoning_output_tokens", "reasoning_output_tokens")):
            require(window["counters"]["gen_ai_sketch_" + metric + "_total"] ==
                    sum(int(r.get(field) or 0) for r in selected),
                    "report usage counter arithmetic differs")
        require(window["recorded_tokens"] == sum(r["prompt_tokens"] + r["completion_tokens"] for r in selected),
                "report recorded token arithmetic differs")
        require(window["quality"]["failed_records"] == sum(r["status"] == "failure" for r in selected),
                "report failure arithmetic differs")
        require(window["quality"]["zero_only_origin_unknown"] ==
                sum(r["prompt_tokens"] == 0 and r["completion_tokens"] == 0 for r in selected),
                "report zero-only arithmetic differs")
        require(window["quality"]["component_total_mismatch"] ==
                sum(r["prompt_tokens"] + r["completion_tokens"] != r["total_tokens"] for r in selected),
                "report component/total mismatch arithmetic differs")
    require("recorded_volume_split" not in report, "unclear whole-period split must be withheld")


@contextmanager
def database():
    name = "fleetdiff-spend-" + uuid.uuid4().hex
    volume = name + "-data"
    docker("volume", "create", volume)
    try:
        docker("run", "-d", "--name", name, "--pull=never", "--network=none",
               "--read-only", "--tmpfs", "/tmp:rw,noexec,nosuid,size=64m",
               "--tmpfs", "/var/run/postgresql:rw,noexec,nosuid,size=16m",
               "--mount", f"type=volume,src={volume},dst=/var/lib/postgresql/data",
               "--mount", f"type=bind,src={ROOT},dst=/example,readonly",
               "--pids-limit=128", "--memory=512m", "--log-driver=none",
               "-e", "POSTGRES_DB=fixture", "-e", "POSTGRES_HOST_AUTH_METHOD=trust",
               PINS["postgres"], "-c", "listen_addresses=",
               "-c", "log_statement=none", "-c", "log_min_error_statement=panic")
        deadline = time.monotonic() + 60
        while True:
            # The entrypoint's temporary init server also passes pg_isready.
            process = run(["docker", "exec", name, "cat", "/proc/1/comm"], timeout=10)
            ready = run(["docker", "exec", name, "pg_isready", "-U", "postgres",
                         "-d", "fixture"], timeout=10)
            if process.stdout.strip() == b"postgres" and ready.returncode == 0:
                break
            require(time.monotonic() < deadline, "test PostgreSQL did not become ready")
            time.sleep(0.25)
        version = docker("exec", name, "postgres", "--version")
        require(version.startswith(("postgres (PostgreSQL) " +
                                    PINS["postgres_version"]).encode()),
                "PostgreSQL version does not match image pin")
        yield name
    finally:
        # Fixed generated names only; do not prune or touch unrelated Docker data.
        run(["docker", "rm", "-f", name])
        cleaned = run(["docker", "volume", "rm", volume])
        require(cleaned.returncode == 0, "test named-volume cleanup failed")


def psql(container, *args, data=None, reader=False):
    return docker("exec", "-i", container, "psql", "-Xq", "-v", "ON_ERROR_STOP=1",
                  "-U", "fixture_reader" if reader else "postgres", "-d", "fixture",
                  *args, data=data)


def writer_payloads():
    encoded = docker(
        "run", "--rm", "--pull=never", "--network=none", "--read-only",
        "--cap-drop=ALL", "--security-opt=no-new-privileges", "--user=65532:65532",
        "--tmpfs", "/tmp:rw,noexec,nosuid,size=64m", "--memory=1g", "--pids-limit=256",
        "--log-driver=none", "--mount", f"type=bind,src={ROOT},dst=/example,readonly",
        "-e", "HOME=/tmp", "-e", "PYTHONDONTWRITEBYTECODE=1",
        "-e", "LITELLM_LOCAL_MODEL_COST_MAP=True", "-e", "LITELLM_TELEMETRY=False",
        "--entrypoint", "python", PINS["litellm"], "/example/writer_probe.py")
    return json.loads(encoded)


def seed_database(container, records):
    psql(container, "-f", "/example/test-schema.sql")
    fields = (*COLUMNS, "metadata", "messages", "response", "proxy_server_request")
    buffer = io.StringIO()
    buffer.write('COPY public."LiteLLM_SpendLogs" (' +
                 ",".join('"' + key + '"' for key in fields) +
                 ") FROM STDIN WITH (FORMAT CSV);\n")
    writer = csv.DictWriter(buffer, fieldnames=fields, lineterminator="\n")
    for record in records:
        row = dict(record)
        for key in fields[len(COLUMNS):]:
            if row[key] is not None and not isinstance(row[key], str):
                row[key] = json.dumps(row[key])
        writer.writerow(row)
    buffer.write("\\.\n")
    psql(container, data=buffer.getvalue().encode())
    # Synthetic DB-only content demonstrates that SELECT * would be unsafe.
    psql(container, data=b'''UPDATE public."LiteLLM_SpendLogs" SET
        messages='{"content":"SENTINEL_PROMPT_73b9"}',
        response='{"content":"SENTINEL_RESPONSE_73b9"}',
        proxy_server_request='{"code":"SENTINEL_CODE_73b9()", "path":"/SENTINEL_PATH_73b9/private.py"}';
    ''')


def export_database(container, start="2026-10-07T00:00:00Z", end="2026-10-09T00:00:00Z", details=False):
    exports = {}
    for fmt in ("csv", "jsonl"):
        directory = "/tmp/export-" + uuid.uuid4().hex
        docker("exec", container, "mkdir", "-m", "700", directory)
        arguments = ["-v", "start_utc=" + start, "-v", "end_utc=" + end,
                     "-v", "csv_file=" + directory + "/spend.csv"]
        if fmt == "jsonl":
            # The JSONL selector must suppress CSV even when both are specified.
            arguments.extend(("-v", "jsonl_file=" + directory + "/spend.jsonl"))
        if details:
            arguments.extend(("-v", "usage_details=true"))
        output = psql(container, *arguments, "-f", "/example/export.sql", reader=True)
        require(not output.strip(), "SQL export leaked output outside its selected file")
        files = docker("exec", container, "ls", "-1", directory).splitlines()
        require(files == [("spend." + fmt).encode()], "SQL created an unexpected output file")
        exports[fmt] = docker("exec", container, "cat", directory + "/spend." + fmt)
    # Reader parity still covers JSON arrays, assembled locally from the CSV.
    exports["json"] = json.dumps(list(csv.DictReader(
        io.StringIO(exports["csv"].decode("utf-8"))))).encode("utf-8")
    return exports


class FixtureTests(unittest.TestCase):
    def test_sql_recipe_uses_one_unsorted_export_without_array_aggregation(self):
        sql = (ROOT / "export.sql").read_text().upper()
        require(sql.count("COPY (") == 1, "SQL recipe must have one CSV COPY pass")
        require("ORDER BY" not in sql, "SQL export must not require a database sort")
        require("JSON_AGG" not in sql and "JSONB_AGG" not in sql,
                "SQL export must not aggregate a JSON array")
        require("JSON_FILE" not in sql, "SQL recipe must not export a JSON array")

    def test_reviewed_arithmetic_and_format_equality(self):
        fixtures = [decode((ROOT / ("synthetic." + fmt)).read_bytes(), fmt) for fmt in FORMATS]
        require(fixtures[0] == fixtures[1] == fixtures[2], "fixture formats differ")
        require(len(fixtures[0]) == 20, "reviewed fixture must have 20 rows")
        for day, requests, tokens, zeros, failed in ((7, 8, 8000, 2, 1), (8, 12, 16000, 4, 2)):
            selected = [r for r in fixtures[0] if r["startTime"].startswith(f"2026-10-{day:02}")]
            require(len(selected) == requests, "fixture request arithmetic changed")
            require(sum(r["prompt_tokens"] + r["completion_tokens"] for r in selected) == tokens,
                    "fixture token arithmetic changed")
            require(sum(r["total_tokens"] == 0 for r in selected) == zeros, "zero arithmetic changed")
            require(sum(r["status"] == "failure" for r in selected) == failed, "failure arithmetic changed")

    def test_scale_generator_is_deterministic_and_streaming(self):
        for fmt in FORMATS:
            a, b = io.StringIO(), io.StringIO()
            write_rows(a, rows(41), fmt)
            write_rows(b, rows(41), fmt)
            require(a.getvalue() == b.getvalue(), "scale generation is not deterministic")
            parsed = decode(a.getvalue().encode(), fmt)
            require(len({r["request_id"] for r in parsed}) == 41, "scale IDs collide")
        require(iter(rows(1_000_000)) is not None, "scale records must be iterable")

    def test_high_cardinality_and_shared_period_keys(self):
        unique = list(rows(100, unique_keys=True))
        require(len({r["api_key"] for r in unique}) == 100, "unique scale keys collide")
        shared = list(rows(100, key_cardinality=20))
        keysets = [{r["api_key"] for r in shared if r["startTime"][:10] == day}
                   for day in ("2026-10-07", "2026-10-08")]
        require(len(keysets[0]) == 20 and keysets[0] == keysets[1], "scale period keys do not overlap")

    def test_generator_output_is_private_and_exclusive(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as temp:
            output = Path(temp) / "generated.csv"
            command = [sys.executable, "-B", str(ROOT / "generate.py"), "--rows", "41",
                       "--unique-keys", "--output", str(output)]
            result = run(command)
            require(result.returncode == 0 and not result.stdout, "file generation failed")
            require(output.stat().st_mode & 0o777 == 0o600, "scale file permissions are not private")
            first = output.read_bytes()
            require(len(decode(first, "csv")) == 41, "scale output row count differs")
            result = run(command)
            require(result.returncode != 0 and output.read_bytes() == first, "generator overwrote output")
            assert_private(result, (output,))

    def test_privacy_guard_never_replays_sentinel(self):
        for data in (b"SENTINEL_CODE_73b9()", b"/SENTINEL_PATH_73b9/private.py",
                     b"synthetic-model-A", SECRET.encode()):
            result = subprocess.CompletedProcess([], 1, data, b"")
            with self.assertRaisesRegex(AssertionError, "output privacy sentinel detected"):
                assert_private(result)
        with mock.patch("subprocess.run", side_effect=OSError("SENTINEL_PATH_73b9")):
            with self.assertRaisesRegex(AssertionError, "details suppressed"):
                run(["synthetic-command"])


@unittest.skipUnless(CLI, "set FLEETDIFF_BIN to the source-built CLI")
class ImporterTests(unittest.TestCase):
    def test_sixteen_day_export_selects_complete_weeks(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as temp:
            path = Path(temp) / "weeks.csv"
            records = list(rows(1600, days=16))
            with path.open("w", newline="") as output:
                write_rows(output, iter(records), "csv")
            result = run([CLI, "investigate", "--litellm-spend", str(path), "--format", "json"])
            assert_private(result, (path, path.parent), records)
            require(result.returncode == 0, "16-day export did not select default periods")
            report = json.loads(result.stdout)
            for side, start, end in (("before", "2026-09-24", "2026-10-01"),
                                     ("after", "2026-10-01", "2026-10-08")):
                window = report[side]
                require(window["period"] == {"start": start + "T00:00:00Z",
                                             "end_exclusive": end + "T00:00:00Z"},
                        "default weekly boundaries differ")
                require(window["logged_model_requests"] == 700 and window["recorded_tokens"] == 840000,
                        "default weekly arithmetic differs")
            require(report["outside_period_rows"] == 200, "partial edge days were included")

    def test_readme_selected_output_matches_run(self):
        readme = (ROOT / "README.md").read_text()
        block = readme.split("```text\n", 1)[1].split("```", 1)[0]
        result = investigate(ROOT / "synthetic.csv", encoding="text")
        actual = result.stdout.decode().splitlines()
        require(all(line in actual for line in block.splitlines()),
                "README output does not match the source-built report")

    def test_agent_route_change_keeps_all_usage(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as temp:
            for operation in ("anthropic_messages", "aanthropic_messages", "responses", "aresponses"):
                for count in (6, 12):
                    records = list(rows())
                    for row in records[8:8+count]:
                        row["call_type"] = operation
                    path = Path(temp) / "agents.json"
                    path.write_text(json.dumps(records))
                    report = investigate(path, records=records)
                    require(report["before"]["recorded_tokens"] == 8000 and
                            report["after"]["recorded_tokens"] == 16000 and
                            not report["comparison_incomplete"],
                            "agent route change created a false usage drop")

    def test_fixture_reports_equal_for_every_group(self):
        records = list(rows())
        for group in GROUPS:
            for hashes in (False, True):
                reports = [investigate(ROOT / ("synthetic." + fmt), group=group,
                                       hashes=hashes, records=records) for fmt in FORMATS]
                require(reports[0] == reports[1] == reports[2], "fixture importer reports differ")
                assert_arithmetic(reports[0], records)
        investigate(ROOT / "synthetic.csv", encoding="text", records=records)

    def test_sentinels_and_rejected_input_diagnostics(self):
        with tempfile.TemporaryDirectory(prefix="SENTINEL_PATH_73b9-", dir="/tmp") as temp:
            root = Path(temp)
            originals = list(rows())
            for row in originals:
                for field in ("api_key", "user", "team_id", "organization_id", "end_user",
                              "model", "model_id", "model_group", "session_id"):
                    row[field] = "SENTINEL_" + field + "_73b9"
                row.update(messages={"code": "SENTINEL_CODE_73b9()"},
                           response={"text": "SENTINEL_RESPONSE_73b9"},
                           proxy_server_request={"path": "/SENTINEL_PATH_73b9/private.py"})
            path = root / "private.json"
            path.write_text(json.dumps(originals))
            before = hashlib.sha256(path.read_bytes()).digest()
            for group in GROUPS:
                for fmt in ("json", "text"):
                    investigate(path, group=group, encoding=fmt, hashes=True, records=originals)
            require(hashlib.sha256(path.read_bytes()).digest() == before, "source file was modified")
            require(list(root.iterdir()) == [path], "importer wrote unexpected artifacts")

            for field, value in (("prompt_tokens", "SENTINEL_NUMBER_73b9"),
                                 ("spend", "SENTINEL_SPEND_73b9"),
                                 ("startTime", "SENTINEL_TIME_73b9")):
                invalid = [dict(originals[0], **{field: value}), *originals[1:]]
                path.write_text(json.dumps(invalid))
                for fmt in ("text", "json"):
                    investigate(path, encoding=fmt, success=field != "startTime", records=invalid)
            for data in (b'{"SENTINEL_CODE_73b9":',
                         json.dumps([originals[0], originals[0]]).encode(),
                         json.dumps({"metadata": {"scope": "daily"}, "data": [
                             {"Date": "2026-10-07", "Spend ($)": "0.0000",
                              "Model": "SENTINEL_MODEL_73b9"}]}).encode()):
                path.write_bytes(data)
                for fmt in ("text", "json"):
                    investigate(path, encoding=fmt, success=False, records=originals)
            csv_path = root / "private.csv"
            csv_path.write_text('Date,Model,Spend ($),Total Tokens\n2026-10-07,SENTINEL_MODEL_73b9,0.0000,1\n')
            investigate(csv_path, success=False)
            investigate(root / "SENTINEL_MISSING_73b9.json", success=False)

            originals[-1]["call_type"] = "SENTINEL_CUSTOM_ROUTE_73b9"
            path.write_text(json.dumps(originals))
            for fmt in ("text", "json"):
                investigate(path, encoding=fmt, records=originals)


@unittest.skipUnless(DOCKER, "set FLEETDIFF_SPEND_DOCKER=1 for the pinned-image DB test")
class DatabaseTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        for key in ("litellm", "postgres"):
            inspected = json.loads(docker("image", "inspect", PINS[key]))
            require(any(ref.endswith(PINS[key].split("@", 1)[1]) for ref in
                        inspected[0].get("RepoDigests", [])), "local image digest mismatch")
        cls.records = writer_payloads()
        cls.db_context = database()
        cls.db = cls.db_context.__enter__()
        cls.addClassCleanup(cls.db_context.__exit__, None, None, None)
        seed_database(cls.db, cls.records)
        cls.exports = export_database(cls.db)
        cls.detail_exports = export_database(cls.db, details=True)

    def test_jsonl_selector_does_not_require_csv_destination(self):
        directory = "/tmp/export-" + uuid.uuid4().hex
        docker("exec", self.db, "mkdir", "-m", "700", directory)
        output = psql(self.db, "-v", "start_utc=2026-10-07T00:00:00Z",
                      "-v", "end_utc=2026-10-09T00:00:00Z",
                      "-v", "jsonl_file=" + directory + "/spend.jsonl",
                      "-f", "/example/export.sql", reader=True)
        require(not output.strip(), "JSONL-only export leaked output")
        require(docker("exec", self.db, "ls", "-1", directory).splitlines() == [b"spend.jsonl"],
                "JSONL-only export created an unexpected file")
        data = docker("exec", self.db, "cat", directory + "/spend.jsonl")
        expected = sorted(decode(self.exports["csv"], "csv"), key=lambda r: r["request_id"])
        require(sorted(decode(data, "jsonl"), key=lambda r: r["request_id"]) == expected,
                "JSONL-only export changed projected records")

    def test_real_database_projection_and_utc_boundaries(self):
        parsed = [sorted(decode(self.exports[fmt], fmt), key=lambda r: r["request_id"])
                  for fmt in FORMATS]
        require(parsed[0] == parsed[1] == parsed[2], "database export formats differ")
        start = datetime(2026, 10, 7, tzinfo=timezone.utc)
        end = datetime(2026, 10, 9, tzinfo=timezone.utc)
        expected = [r for r in self.records if
                    start <= datetime.fromisoformat(r["startTime"].replace("Z", "+00:00")) < end]
        expected.sort(key=lambda r: r["request_id"])
        require(OPERATIONS <= {r["call_type"] for r in expected},
                "writer probe omits supported operation coverage")
        require(len(parsed[0]) == len(expected), "database export row count differs")
        for actual, original in zip(parsed[0], expected):
            require(set(actual) == set(COLUMNS), "SQL selected an unexpected column")
            for field in COLUMNS:
                if field not in ("startTime", "endTime", "spend"):
                    require(actual[field] == (original[field] if original[field] is not None else ""),
                            "database projection differs from writer payload")
            require(Decimal(actual["spend"]) == Decimal(str(original["spend"])),
                    "SQL changed recorded spend")
            for field in ("startTime", "endTime"):
                parsed_time = datetime.fromisoformat(actual[field].replace("Z", "+00:00"))
                original_time = datetime.fromisoformat(original[field].replace("Z", "+00:00"))
                require(parsed_time == original_time.astimezone(timezone.utc),
                        "SQL changed the timestamp instant")
            require(actual["startTime"].endswith("Z") and actual["endTime"].endswith("Z"),
                    "SQL export timestamps are not explicit UTC")
        for data in self.exports.values():
            require(b"SENTINEL_CODE_" not in data and b"SENTINEL_PATH_" not in data and
                    b"SENTINEL_PROMPT_" not in data and b"SENTINEL_RESPONSE_" not in data,
                    "SQL exported content fields")
        empty = export_database(self.db, "2026-09-01T00:00:00Z", "2026-09-02T00:00:00Z")
        require(all(decode(empty[fmt], fmt) == [] for fmt in FORMATS), "empty SQL export is invalid")

    def test_optional_aliases_come_from_actual_writer_metadata(self):
        paths = {"cache_read_input_tokens": ("cache_read_input_tokens",),
                 "cache_write_input_tokens": ("cache_creation_input_tokens",),
                 "reasoning_output_tokens": ("completion_tokens_details", "reasoning_tokens")}
        parsed = []
        for fmt in FORMATS:
            records = sorted(decode(self.detail_exports[fmt], fmt), key=lambda r: r["request_id"])
            for row in records:
                require(set(row) == set(COLUMNS) | set(paths), "SQL selected an unexpected detail column")
                for alias in paths:
                    row[alias] = None if row[alias] in (None, "") else int(row[alias])
            parsed.append(records)
        require(parsed[0] == parsed[1] == parsed[2], "optional detail formats differ")
        originals = {r["request_id"]: r for r in self.records}
        plain = {r["request_id"]: r for r in decode(self.exports["csv"], "csv")}
        require(len(parsed[0]) == len(plain), "optional export row count differs")
        require({r["request_id"] for r in parsed[0]} == set(plain) <= set(originals),
                "optional export membership differs from writer records")
        for row in parsed[0]:
            require({field: row[field] for field in COLUMNS} == plain[row["request_id"]],
                    "optional projection changed base fields")
            metadata = originals[row["request_id"]]["metadata"]
            if isinstance(metadata, str):
                metadata = json.loads(metadata)
            for alias, path in paths.items():
                value = (metadata or {}).get("additional_usage_values")
                for key in path:
                    value = value.get(key) if isinstance(value, dict) else None
                require(row[alias] == (None if value is None else int(value)),
                        "optional usage values differ from actual writer metadata")

    @unittest.skipUnless(CLI, "set FLEETDIFF_BIN for the DB-export-to-importer check")
    def test_real_database_exports_through_importer(self):
        with tempfile.TemporaryDirectory(prefix="SENTINEL_PATH_db-", dir="/tmp") as temp:
            for exports in (self.exports, self.detail_exports):
                paths = []
                for fmt in FORMATS:
                    path = Path(temp) / ("export." + fmt)
                    path.write_bytes(exports[fmt])
                    paths.append(path)
                for group in GROUPS:
                    for hashes in (False, True):
                        reports = [investigate(path, group=group, hashes=hashes,
                                               records=self.records) for path in paths]
                        require(reports[0] == reports[1] == reports[2],
                                "database export importer reports differ")
                        assert_arithmetic(reports[0], decode(exports["json"], "json"))
                for path in paths:
                    investigate(path, encoding="text", records=self.records)


if __name__ == "__main__":
    unittest.main()
