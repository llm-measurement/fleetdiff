# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Offline pinned writer/PostgreSQL byte oracle for the native exporter.

FLEETDIFF_SPEND_DOCKER=1 FLEETDIFF_LINUX_BIN=/absolute/linux/fleetdiff \
  python3 -B -m unittest discover -s examples/litellm-spend \
  -p test_native_export.py -v

Uses existing local image pins only. The binary is mounted read-only at
/native/fleetdiff, not executed from the database container's noexec /tmp.
Spend usage comes from the actual pinned writer. Daily counters replay the
existing recorded runtime fixture; this is export parity, not a new proxy run.
Failures never include subprocess output, credentials, rows, or byte diffs.
FLEETDIFF_NATIVE_EXPORT_LARGE=1 additionally enables a 10,000,001-row test
requiring multi-GB Docker volume space; it does not fit the fixture's /tmp.
"""
from contextlib import contextmanager, ExitStack
import csv
import io
import json
import os
from pathlib import Path
import re
import subprocess
import time
import unittest
from unittest import mock
from urllib.parse import urlencode
import uuid

import test_spend as spend


LINUX_BIN = os.environ.get("FLEETDIFF_LINUX_BIN")
URL = "postgres://fixture_reader@/fixture?host=/var/run/postgresql&sslmode=disable"
START, END = "2026-10-07T00:00:00Z", "2026-10-09T00:00:00Z"
PASSWORD = "SENTINEL_NATIVE_PASSWORD_20261018"
FIELD_BYTES = 8 << 10  # internal/spend.MaxFieldBytes, measured in UTF-8 bytes.
MAX_ROWS = 10_000_000
FINAL_FILES = {"spend.csv", "spend.jsonl", "daily.csv"}
require = spend.require
docker, psql, run = spend.docker, spend.psql, spend.run


@contextmanager
def native_database(binary):
    """Compose the existing pinned fixture with one exec-capable binary mount."""
    def mounted(*args, **kwargs):
        if args and args[0] == "run":
            index = args.index(spend.PINS["postgres"])
            args = (*args[:index], "--mount",
                    f"type=bind,src={binary},dst=/native/fleetdiff,readonly", *args[index:])
        return docker(*args, **kwargs)

    with ExitStack() as cleanup:
        # Patch only fixture startup; setup/cleanup remains in the existing helper.
        with mock.patch.object(spend, "docker", side_effect=mounted):
            container = cleanup.enter_context(spend.database())
        yield container


def private_file(container, path, data, mode="600"):
    docker("exec", "-i", container, "sh", "-c",
           'umask 077; cat > "$1" && chmod "$2" "$1"', "fixture", path, mode,
           data=data)


def scalar(container, sql):
    return psql(container, "-At", "-c", sql).strip()


def wait_for(container, sql, message, process=None):
    deadline = time.monotonic() + 15
    while scalar(container, sql) != b"t":
        require(process is None or process.poll() is None,
                "test process exited before database synchronization")
        require(time.monotonic() < deadline, message)
        time.sleep(0.05)  # Poll explicit database state; never infer readiness.


def finish(process, timeout=20):
    try:
        stdout, stderr = process.communicate(timeout=timeout)
    except (OSError, subprocess.SubprocessError):
        raise AssertionError("test process did not finish; details suppressed") from None
    return subprocess.CompletedProcess([], process.returncode, stdout, stderr)


@contextmanager
def background(command):
    try:
        process = subprocess.Popen(command, stdin=subprocess.DEVNULL,
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    except (OSError, subprocess.SubprocessError):
        raise AssertionError("test process could not start; details suppressed") from None
    try:
        yield process
    finally:
        if process.poll() is None:
            process.kill()
        finish(process)


def assert_bytes(actual, expected, label):
    require(actual == expected, label + " bytes differ from the canonical psql export")


def apply_documented_grants(container):
    document = (spend.ROOT.parents[1] / "docs" / "NATIVE_EXPORT.md").read_text()
    fence = chr(96) * 3
    blocks = re.findall(fence + r"sql\n(.*?)" + fence, document, re.DOTALL)
    grants = re.findall(
        r'GRANT SELECT\s*\([^;]+\)\s+ON TABLE public\."LiteLLM_(?:SpendLogs|DailyUserSpend)"'
        r"\s+TO fleetdiff_reader;", "\n".join(blocks))
    require(len(grants) == 2 and
            sum('public."LiteLLM_SpendLogs"' in grant for grant in grants) == 1,
            "documented export column grants are missing or ambiguous")
    psql(container, "-c", '''REVOKE SELECT ON TABLE public."LiteLLM_SpendLogs",
        public."LiteLLM_DailyUserSpend" FROM fixture_reader;''' +
         "\n".join(grant.replace("TO fleetdiff_reader;", "TO fixture_reader;") for grant in grants))
    require(scalar(container, '''SELECT
        NOT has_table_privilege('fixture_reader', 'public."LiteLLM_SpendLogs"', 'SELECT')
        AND NOT has_table_privilege('fixture_reader', 'public."LiteLLM_DailyUserSpend"', 'SELECT')
        AND NOT has_column_privilege('fixture_reader', 'public."LiteLLM_SpendLogs"', 'messages', 'SELECT')
        AND NOT has_column_privilege('fixture_reader', 'public."LiteLLM_SpendLogs"', 'response', 'SELECT')
        AND NOT has_column_privilege('fixture_reader', 'public."LiteLLM_SpendLogs"', 'proxy_server_request', 'SELECT')
        ''') == b"t", "documented grants retain broad or content-column access")


@unittest.skipUnless(spend.DOCKER and LINUX_BIN,
                     "set FLEETDIFF_SPEND_DOCKER=1 and FLEETDIFF_LINUX_BIN")
class NativeExportTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        try:
            binary = Path(LINUX_BIN).resolve(strict=True)
            with binary.open("rb") as source:
                magic = source.read(4)
            require(binary.is_file() and os.access(binary, os.X_OK) and magic == b"\x7fELF",
                    "FLEETDIFF_LINUX_BIN must be an executable Linux ELF file")
        except OSError:
            raise AssertionError("cannot read the Linux test binary; details suppressed") from None
        for key in ("litellm", "postgres"):
            inspected = json.loads(docker("image", "inspect", spend.PINS[key]))
            require(any(ref.endswith(spend.PINS[key].split("@", 1)[1])
                        for ref in inspected[0].get("RepoDigests", [])),
                    "local test image digest mismatch")
        cls.records = (spend.writer_payloads() + spend.writer_payloads("--routing") +
                       spend.writer_payloads("--rejections"))
        context = native_database(binary)
        cls.db = context.__enter__()
        cls.addClassCleanup(context.__exit__, None, None, None)
        state = json.loads(docker("inspect", cls.db))[0]
        require(state["HostConfig"]["NetworkMode"] == "none",
                "native fixture must have no external network")
        require(any(m["Destination"] == "/native/fleetdiff" and not m["RW"]
                    for m in state["Mounts"]), "native binary is not mounted read-only")

    def setUp(self):
        # Malformed projection controls are isolated by resetting the schema.
        psql(self.db, data=b'''DROP SCHEMA public CASCADE; CREATE SCHEMA public;
            DO $$ BEGIN
              IF EXISTS (SELECT FROM pg_roles WHERE rolname='fixture_reader') THEN
                DROP OWNED BY fixture_reader; DROP ROLE fixture_reader;
              END IF;
            END $$;''')
        spend.seed_database(self.db, list(reversed(self.records)))
        # Pinned Prisma: date String; all three counters BigInt, not Int.
        psql(self.db, data=b'''CREATE TABLE public."LiteLLM_DailyUserSpend" (
            date text NOT NULL, api_requests bigint NOT NULL DEFAULT 0,
            successful_requests bigint NOT NULL DEFAULT 0,
            failed_requests bigint NOT NULL DEFAULT 0);
            GRANT SELECT ON public."LiteLLM_DailyUserSpend" TO fixture_reader;
            ALTER ROLE fixture_reader SET default_transaction_read_only=off;
            ALTER ROLE fixture_reader SET default_transaction_isolation='read committed';
            ALTER ROLE fixture_reader SET timezone='Pacific/Honolulu';''')
        daily = list(csv.reader(io.StringIO(
            (spend.ROOT / "coverage_runtime_daily.csv").read_text())))[1:]
        data = io.StringIO()
        data.write('COPY public."LiteLLM_DailyUserSpend" FROM STDIN WITH (FORMAT CSV);\n')
        csv.writer(data, lineterminator="\n").writerows(reversed(daily))
        data.write("\\.\n")
        psql(self.db, data=data.getvalue().encode())
        self.parent = "/tmp/native-" + uuid.uuid4().hex
        docker("exec", self.db, "mkdir", "-m", "700", self.parent)
        self.addCleanup(docker, "exec", self.db, "rm", "-rf", self.parent)

    def destination(self):
        return self.parent + "/out-" + uuid.uuid4().hex

    def command(self, directory, *, fmt=None, daily=False, routing=False, usage=True,
                credential_file=None, url=URL, environment=None, start=START, end=END,
                extra=(), pid_file=None, user=None):
        env = {"PATH": "/usr/local/bin:/usr/bin:/bin", "HOME": self.parent,
               "LC_ALL": "C", "TZ": "Pacific/Honolulu"}
        if url is not None:
            env["FLEETDIFF_LITELLM_DATABASE_URL"] = url
        env.update(environment or {})
        args = ["/native/fleetdiff", "export", "litellm", "--out", directory,
                "--from", start, "--to", end]
        if fmt is not None:
            args.extend(("--format", fmt))
        if daily:
            args.append("--daily")
        if routing:
            args.append("--routing-details")
        if usage is not None:
            args.append("--usage-details=" + str(usage).lower())
        if credential_file is not None:
            args.extend(("--database-url-file", credential_file))
        args.extend(extra)
        # A permissive umask exercises the exporter's own permission policy.
        script = 'umask 000; exec "$@"'
        shell_args = []
        if pid_file:
            script = 'umask 000; printf "%s\\n" "$$" > "$1"; shift; exec "$@"'
            shell_args.append(pid_file)
        return ["docker", "exec", *(["--user", user] if user else []), self.db, "env", "-i",
                *(key + "=" + value for key, value in env.items()),
                "sh", "-c", script, "native-export", *shell_args, *args]

    def check_result(self, result, success):
        spend.assert_private(result, records=self.records)
        require(PASSWORD.encode() not in result.stdout + result.stderr,
                "credential appeared in native export diagnostics")
        require((result.returncode == 0) == success, "unexpected native export exit status")
        if not success:
            require(not result.stdout, "failed native export emitted success output")

    def assert_no_complete_outputs(self, directory):
        result = run(["docker", "exec", self.db, "sh", "-c",
                      'if [ -d "$1" ]; then ls -A1 -- "$1"; fi', "fixture", directory])
        require(result.returncode == 0, "cannot inspect failed export output")
        require(not FINAL_FILES.intersection(result.stdout.decode().splitlines()),
                "failed or interrupted export published a complete output")

    def assert_no_output_directory(self, directory):
        result = run(["docker", "exec", self.db, "sh", "-c",
                      '[ ! -e "$1" ] && [ ! -L "$1" ]', "fixture", directory])
        require(result.returncode == 0, "failed or canceled export left its output directory")

    def read_export(self, directory, fmt="csv", daily=False):
        expected = {"spend." + fmt} | ({"daily.csv"} if daily else set())
        names = set(docker("exec", self.db, "ls", "-A1", directory).decode().splitlines())
        require(names == expected, "native export created missing or unexpected files")
        require(docker("exec", self.db, "stat", "-c", "%a:%u", directory).strip() == b"700:0",
                "native output directory is not owned mode 0700")
        files, inodes = {}, set()
        for name in sorted(expected):
            path = directory + "/" + name
            require(docker("exec", self.db, "stat", "-c", "%a:%u", path).strip() == b"600:0",
                    "native output file is not owned mode 0600")
            inodes.add(docker("exec", self.db, "stat", "-c", "%d:%i", path).strip())
            files[name] = docker("exec", self.db, "cat", path)
            for sentinel in (b"SENTINEL_PROMPT_", b"SENTINEL_RESPONSE_", b"SENTINEL_CODE_",
                             b"SENTINEL_PATH_", PASSWORD.encode()):
                require(sentinel not in files[name], "unprojected private content escaped into export")
        require(len(inodes) == len(expected), "native output files alias each other")
        return files

    def export(self, *, success=True, directory=None, timeout=40, **options):
        directory = directory or self.destination()
        result = run(self.command(directory, **options), timeout=timeout)
        self.check_result(result, success)
        if not success:
            self.assert_no_complete_outputs(directory)
            return None
        return self.read_export(directory, options.get("fmt") or "csv", options.get("daily", False))

    def daily_oracle(self, start=START, end=END):
        directory = self.destination()
        result = run(["docker", "exec", "-e",
                      "PGSERVICEFILE=/example/coverage_runtime_pg_service.conf", self.db,
                      "sh", "/example/export-coverage.sh", directory, start, end, "coverage_fixture"])
        spend.assert_private(result, records=self.records)
        require(result.returncode == 0, "guarded canonical daily export failed")
        for name in ("private-psql.out", "private-psql.err"):
            require(not docker("exec", self.db, "cat", directory + "/" + name),
                    "guarded canonical export produced private diagnostics")
        return {name: docker("exec", self.db, "cat", directory + "/" + name)
                for name in ("spend.csv", "daily.csv")}

    def test_all_format_routing_usage_combinations_exact_bytes(self):
        daily = self.daily_oracle()["daily.csv"]
        for routing in (False, True):
            for usage in (False, True):
                oracle = spend.export_database(self.db, details=usage, routing=routing)
                for fmt in ("csv", "jsonl"):
                    for include_daily in (False, True):
                        with self.subTest(format=fmt, routing=routing, usage=usage, daily=include_daily):
                            actual = self.export(fmt=fmt, daily=include_daily, routing=routing, usage=usage)
                            assert_bytes(actual["spend." + fmt], oracle[fmt], "spend")
                            if include_daily:
                                assert_bytes(actual["daily.csv"], daily, "daily")
        default = self.export(usage=None)
        assert_bytes(default["spend.csv"], spend.export_database(self.db)["csv"], "default spend")

    def test_exact_documented_column_grants_preserve_export_bytes(self):
        expected = spend.export_database(self.db, details=True, routing=True)
        daily = self.daily_oracle()["daily.csv"]
        apply_documented_grants(self.db)
        for fmt in ("csv", "jsonl"):
            actual = self.export(fmt=fmt, daily=True, routing=True)
            assert_bytes(actual["spend." + fmt], expected[fmt], "column-granted spend")
            assert_bytes(actual["daily.csv"], daily, "column-granted daily")

    def test_half_open_bounds_empty_windows_and_repeatability(self):
        for start, end in ((START, "2026-10-08T00:00:00Z"),
                           ("2026-10-08T00:00:00Z", END),
                           ("2026-10-06T23:59:59.999Z", START),
                           ("2026-10-10T00:00:00Z", "2026-10-11T00:00:00Z")):
            oracle = spend.export_database(self.db, start, end)
            for fmt in ("csv", "jsonl"):
                with self.subTest(format=fmt, start=start):
                    first = self.export(fmt=fmt, start=start, end=end)
                    second = self.export(fmt=fmt, start=start, end=end)
                    assert_bytes(first["spend." + fmt], oracle[fmt], "bounded spend")
                    require(first == second, "repeated native exports changed bytes")

    def test_c_collation_ties_and_csv_json_escaping(self):
        # Projection-only controls change identifiers, not writer-generated usage.
        psql(self.db, data=b'''UPDATE public."LiteLLM_SpendLogs"
            SET request_id=CASE WHEN request_id='synthetic-000' THEN 'Z-tie'
                                WHEN request_id='synthetic-001' THEN 'a-tie'
                                ELSE request_id END,
                "startTime"='2026-10-07 12:34:56.789',
                "endTime"='2026-10-07 12:34:57.789',
                model=E'SENTINEL_ESCAPED,"quoted"\\nline\\r\\n\\t\\\\tail',
                team_id=NULL, organization_id='', "user"=U&'SENTINEL_\\00e9\\96ea';''')
        oracle = spend.export_database(self.db, details=True, routing=True)
        rows = list(csv.DictReader(io.StringIO(oracle["csv"].decode())))
        ids = [row["request_id"] for row in rows]
        require(ids == sorted(ids, key=lambda value: value.encode()),
                "canonical spend ordering is not request_id COLLATE C for timestamp ties")
        for fmt in ("csv", "jsonl"):
            actual = self.export(fmt=fmt, routing=True)
            assert_bytes(actual["spend." + fmt], oracle[fmt], "escaped spend")

    def test_owned_0600_credential_file_and_ambiguous_sources(self):
        path = self.parent + "/SENTINEL_DATABASE_URL"
        private_file(self.db, path, (URL + "\r\n").encode())
        expected = spend.export_database(self.db)["csv"]
        actual = self.export(url=None, credential_file=path)
        assert_bytes(actual["spend.csv"], expected, "credential-file spend")
        self.export(success=False, credential_file=path)
        self.export(success=False, url=None)
        self.export(success=False, url="")
        self.export(success=False, url=None, environment={"DATABASE_URL": URL})
        for mode in ("644", "660", "400", "700"):
            with self.subTest(mode=mode):
                docker("exec", self.db, "chmod", mode, path)
                self.export(success=False, url=None, credential_file=path)
        docker("exec", self.db, "chmod", "600", path)
        docker("exec", self.db, "chown", "65534:65534", path)
        self.export(success=False, url=None, credential_file=path)

    def test_credential_symlink_directory_fifo_and_oversize_rejected(self):
        path = self.parent + "/SENTINEL_CREDENTIAL"
        private_file(self.db, path, URL.encode())
        link = path + "-link"
        docker("exec", self.db, "ln", "-s", path, link)
        fifo = path + "-fifo"
        docker("exec", self.db, "mkfifo", "-m", "600", fifo)
        for candidate in (link, fifo, self.parent, path + "-missing"):
            self.export(success=False, url=None, credential_file=candidate)
        private_file(self.db, path, b"SENTINEL_OVERSIZE" * 5000)
        self.export(success=False, url=None, credential_file=path)

    def test_inherited_pg_environment_and_service_files_are_ignored(self):
        service = self.parent + "/.pg_service.conf"
        private_file(self.db, service, b'''[SENTINEL_SERVICE]
host=/SENTINEL_SOCKET
dbname=SENTINEL_DATABASE
user=SENTINEL_USER
password=SENTINEL_PASSWORD
options=-c search_path=SENTINEL_SCHEMA
''')
        passfile = self.parent + "/.pgpass"
        private_file(self.db, passfile, b"*:*:*:*:SENTINEL_PASSWORD\n")
        poison = {"PGHOST": "/SENTINEL_SOCKET", "PGHOSTADDR": "192.0.2.1",
                  "PGPORT": "1", "PGDATABASE": "SENTINEL_DATABASE", "PGUSER": "SENTINEL_USER",
                  "PGPASSWORD": PASSWORD, "PGSERVICE": "SENTINEL_SERVICE",
                  "PGSERVICEFILE": service, "PGSYSCONFDIR": self.parent, "PGPASSFILE": passfile,
                  "PGSSLMODE": "SENTINEL_INVALID_MODE", "PGSSLROOTCERT": "/SENTINEL_CA",
                  "PGOPTIONS": "-c search_path=SENTINEL_SCHEMA -c default_transaction_read_only=off",
                  "PGCLIENTENCODING": "LATIN1", "PGAPPNAME": "SENTINEL_APPLICATION",
                  "PGCONNECT_TIMEOUT": "1", "DATABASE_URL": "SENTINEL_OTHER_DATABASE"}
        oracle = self.daily_oracle()
        actual = self.export(daily=True, environment=poison)
        require(actual == oracle, "inherited environment or home files altered native export bytes")
        self.export(success=False, url=None, environment=poison)

    def test_pgpass_and_password_environment_cannot_supply_missing_url_password(self):
        # Unlike trust authentication, SCRAM makes implicit-password use observable.
        expected = spend.export_database(self.db)["csv"]
        psql(self.db, "-c", "ALTER ROLE fixture_reader PASSWORD '" + PASSWORD + "'")
        hba = scalar(self.db, "SHOW hba_file").decode()
        require(hba.startswith("/var/lib/postgresql/data/"), "unexpected fixture auth file location")
        original = docker("exec", self.db, "cat", hba)

        def reload_auth(data):
            before = scalar(self.db, "SELECT extract(epoch FROM pg_conf_load_time())").decode()
            require(before.replace(".", "").isdigit(), "invalid config reload observation")
            private_file(self.db, hba, data)
            require(scalar(self.db, "SELECT pg_reload_conf()") == b"t", "test auth reload failed")
            wait_for(self.db, "SELECT extract(epoch FROM pg_conf_load_time()) > " + before,
                     "test auth reload was not observed")

        try:
            reload_auth(b"local fixture fixture_reader scram-sha-256\n" + original)
            explicit = URL.replace("fixture_reader@", "fixture_reader:" + PASSWORD + "@")
            # psql's shared reader helper intentionally has no password. The
            # positive native control proves authentication is otherwise usable.
            actual = self.export(url=explicit)
            assert_bytes(actual["spend.csv"], expected, "password-authenticated spend")
            self.export(success=False)
            passfile = self.parent + "/.pgpass"
            private_file(self.db, passfile, ("*:*:fixture:fixture_reader:" + PASSWORD + "\n").encode())
            for environment in ({}, {"PGPASSFILE": passfile}, {"PGPASSWORD": PASSWORD}):
                self.export(success=False, environment=environment)
            service = self.parent + "/.pg_service.conf"
            private_file(self.db, service, ("[SENTINEL_SERVICE]\n"
                "host=/var/run/postgresql\ndbname=fixture\nuser=fixture_reader\npassword=" +
                PASSWORD + "\nsslmode=disable\n").encode())
            self.export(success=False, environment={"PGSERVICE": "SENTINEL_SERVICE",
                                                    "PGSERVICEFILE": service})
            self.export(url=explicit, environment={"PGPASSWORD": "SENTINEL_WRONG_PASSWORD",
                                                   "PGPASSFILE": passfile})
        finally:
            reload_auth(original)

    def test_invalid_bounds_formats_and_argv_credentials_are_private(self):
        cases = (
            {"start": "SENTINEL_INVALID_BOUND"}, {"start": END, "end": START},
            {"start": START, "end": START}, {"fmt": "SENTINEL_INVALID_FORMAT"},
            {"start": "2026-10-07T00:00:00-04:00"},
            {"daily": True, "start": "2026-10-07T00:00:00.001Z"},
            {"daily": True, "start": "2000-01-01T00:00:00Z"},
            {"extra": ("--database-url", URL.replace("fixture_reader@", "fixture_reader:" + PASSWORD + "@"))},
            {"extra": ("--SENTINEL_UNKNOWN_FLAG",)},
        )
        for index, options in enumerate(cases):
            with self.subTest(invalid_options=index):
                self.export(success=False, **options)

    def test_url_options_and_connection_errors_do_not_leak_sentinels(self):
        for key, value in (("service", "SENTINEL_SERVICE"), ("servicefile", "/SENTINEL_SERVICE"),
                           ("passfile", "/SENTINEL_PASSWORD_FILE"),
                           ("options", "-c search_path=SENTINEL_SCHEMA"),
                           ("hostaddr", "192.0.2.1"), ("sslkey", "/SENTINEL_KEY"),
                           ("application_name", "SENTINEL_APP")):
            with self.subTest(option=key):
                self.export(success=False, url=URL + "&" + urlencode({key: value}))
        for url in (URL + "&sslmode=disable", URL + "#SENTINEL_FRAGMENT",
                    URL.replace("fixture_reader@", "SENTINEL_MISSING:" + PASSWORD + "@"),
                    URL.replace("/fixture?", "/SENTINEL_MISSING_DATABASE?"),
                    "host=/var/run/postgresql user=fixture_reader dbname=fixture password=" + PASSWORD,
                    "https://SENTINEL_HOST/fixture", URL.replace("fixture_reader@", "")):
            self.export(success=False, url=url)

    def test_existing_directory_and_symlink_outputs_are_not_modified(self):
        directory = self.destination()
        original = self.export(directory=directory, daily=True)
        link = self.parent + "/existing-link"
        docker("exec", self.db, "ln", "-s", directory, link)
        dangling = self.parent + "/dangling-link"
        docker("exec", self.db, "ln", "-s", self.parent + "/absent", dangling)
        parent_link = self.parent + "/parent-link"
        docker("exec", self.db, "ln", "-s", self.parent, parent_link)
        for candidate in (directory, link, dangling, parent_link + "/new"):
            result = run(self.command(candidate, daily=True))
            self.check_result(result, False)
            require(self.read_export(directory, daily=True) == original,
                    "rejected export changed preexisting output bytes")
        require(run(["docker", "exec", self.db, "test", "-e", self.parent + "/absent"]).returncode == 1,
                "rejected dangling link created its target")

    def test_schema_type_precision_missing_column_and_primary_key_mismatches(self):
        # Identity-only fill permits testing a wrongly required optional column.
        psql(self.db, "-c", '''UPDATE public."LiteLLM_SpendLogs"
            SET agent_id='' WHERE agent_id IS NULL''')
        cases = (
            ('ALTER COLUMN "startTime" TYPE timestamptz(3) USING "startTime" AT TIME ZONE \'UTC\'',
             'ALTER COLUMN "startTime" TYPE timestamp(3) USING "startTime" AT TIME ZONE \'UTC\''),
            ('ALTER COLUMN "startTime" TYPE timestamp(6)', 'ALTER COLUMN "startTime" TYPE timestamp(3)'),
            ('ALTER COLUMN prompt_tokens TYPE bigint', 'ALTER COLUMN prompt_tokens TYPE integer'),
            ('ALTER COLUMN metadata TYPE json', 'ALTER COLUMN metadata TYPE jsonb'),
            ('RENAME COLUMN agent_id TO "SENTINEL_MISSING_COLUMN"',
             'RENAME COLUMN "SENTINEL_MISSING_COLUMN" TO agent_id'),
            ('DROP CONSTRAINT "LiteLLM_SpendLogs_pkey"', 'ADD PRIMARY KEY (request_id)'),
            ('ALTER COLUMN call_type DROP NOT NULL', 'ALTER COLUMN call_type SET NOT NULL'),
            ('ALTER COLUMN agent_id SET NOT NULL', 'ALTER COLUMN agent_id DROP NOT NULL'),
        )
        for index, (change, restore) in enumerate(cases):
            with self.subTest(schema_case=index):
                psql(self.db, "-c", 'ALTER TABLE public."LiteLLM_SpendLogs" ' + change)
                try:
                    self.export(success=False, daily=True)
                finally:
                    psql(self.db, "-c", 'ALTER TABLE public."LiteLLM_SpendLogs" ' + restore)
        for column in ("date", "api_requests", "successful_requests", "failed_requests"):
            with self.subTest(daily_nullable=column):
                prefix = 'ALTER TABLE public."LiteLLM_DailyUserSpend" ALTER COLUMN ' + column
                psql(self.db, "-c", prefix + " DROP NOT NULL")
                try:
                    self.export(success=False, daily=True)
                finally:
                    psql(self.db, "-c", prefix + " SET NOT NULL")
        psql(self.db, "-c", 'ALTER TABLE public."LiteLLM_DailyUserSpend" ALTER COLUMN date TYPE date USING date::date')
        self.export(success=False, daily=True)
        self.export()  # A daily mismatch must not disable spend-only export.

    def test_database_cast_error_does_not_publish_spend_or_daily(self):
        psql(self.db, data=b'''UPDATE public."LiteLLM_SpendLogs" SET metadata=
            jsonb_set(COALESCE(metadata, '{}'::jsonb), '{additional_usage_values}',
              '{"cache_read_input_tokens":"SENTINEL_BAD_USAGE"}'::jsonb)
            WHERE request_id='synthetic-000';''')
        for fmt in ("csv", "jsonl"):
            self.export(success=False, fmt=fmt, daily=True)
            oracle = spend.export_database(self.db, details=False)
            actual = self.export(fmt=fmt, daily=True, usage=False)
            assert_bytes(actual["spend." + fmt], oracle[fmt], "usage-opt-out spend")

    def test_known_field_limits_apply_to_utf8_bytes(self):
        for width, expression in ((1, "'x'"), (4, "chr(128578)")):
            for delta in (0, 1):
                length = FIELD_BYTES // width + delta
                psql(self.db, "-c", 'UPDATE public."LiteLLM_SpendLogs" SET model=repeat(' +
                     expression + f", {length}) WHERE request_id='synthetic-000'")
                for fmt in ("csv", "jsonl"):
                    with self.subTest(bytes_per_character=width, over_limit=bool(delta), format=fmt):
                        actual = self.export(success=not delta, fmt=fmt, daily=True)
                        if not delta:
                            oracle = spend.export_database(self.db)
                            assert_bytes(actual["spend." + fmt], oracle[fmt], "field-boundary spend")

    @unittest.skipUnless(os.environ.get("FLEETDIFF_NATIVE_EXPORT_LARGE") == "1",
                         "set FLEETDIFF_NATIVE_EXPORT_LARGE=1 for the multi-GB 10M-row gate")
    def test_real_postgresql_row_limit(self):
        disk = docker("exec", self.db, "df", "-Pk", "/var/lib/postgresql/data").splitlines()[-1].split()
        require(len(disk) >= 6 and disk[3].isdigit(), "cannot measure Docker fixture free space")
        if int(disk[3]) * 1024 < 25 * (1 << 30):
            self.skipTest("10M-row gate requires at least 25 GiB free in the Docker data volume")
        # Clone a real writer row; only request_id changes to preserve its PK.
        # First prove this payload works, so malformed-field failures cannot pass.
        for fmt in ("csv", "jsonl"):
            self.export(fmt=fmt)
        fields = (*spend.COLUMNS, "agent_id", "metadata", "messages", "response",
                  "proxy_server_request")
        columns = ",".join('"' + name + '"' for name in fields)
        selected = ",".join(("'native-limit-' || lpad(g::text, 8, '0')"
                             if name == "request_id" else 't."' + name + '"')
                            for name in fields)
        sql = f'''CREATE TEMP TABLE native_limit_source AS
            SELECT * FROM public."LiteLLM_SpendLogs" WHERE request_id='synthetic-000';
            TRUNCATE public."LiteLLM_SpendLogs";
            INSERT INTO public."LiteLLM_SpendLogs" ({columns})
            SELECT {selected} FROM generate_series(1, {MAX_ROWS + 1}) g
            CROSS JOIN native_limit_source t;
            CREATE INDEX native_limit_order ON public."LiteLLM_SpendLogs"
                ("startTime", request_id COLLATE "C");
            ANALYZE public."LiteLLM_SpendLogs";'''
        # The ordinary psql helper has a 90-second subprocess cap, too short for
        # deliberately loading this scale. Keep its exact psql options here.
        started = time.monotonic()
        docker("exec", "-i", self.db, "psql", "-Xq", "-v", "ON_ERROR_STOP=1",
               "-U", "postgres", "-d", "fixture", data=sql.encode(), timeout=900)
        seed_seconds = time.monotonic() - started
        require(scalar(self.db, 'SELECT count(*) FROM public."LiteLLM_SpendLogs"') ==
                str(MAX_ROWS + 1).encode(), "large row-limit fixture has the wrong size")
        source_bytes = scalar(self.db, """SELECT pg_total_relation_size('public."LiteLLM_SpendLogs"')""")
        require(source_bytes.isdigit(), "cannot measure large fixture storage")
        print(f"native large fixture: rows={MAX_ROWS + 1} source_bytes={int(source_bytes)} "
              f"seed_seconds={seed_seconds:.2f}", flush=True)
        parent = "/var/lib/postgresql/data/native-limit-" + uuid.uuid4().hex
        docker("exec", "--user", "postgres", self.db, "mkdir", "-m", "700", parent)
        self.addCleanup(docker, "exec", self.db, "rm", "-rf", parent)
        for fmt in ("csv", "jsonl"):
            with self.subTest(format=fmt):
                directory = parent + "/" + fmt
                started = time.monotonic()
                try:
                    result = run(self.command(directory, fmt=fmt, user="postgres"), timeout=330)
                finally:
                    print(f"native large export: format={fmt} "
                          f"elapsed_seconds={time.monotonic() - started:.2f}", flush=True)
                self.check_result(result, False)
                self.assert_no_complete_outputs(directory)
                self.assert_no_output_directory(directory)
                require(b"request, file, row or field limit" in result.stderr,
                        "large fixture failed before reaching the export limit")

    def install_gate(self, key, *, action=None):
        if action is None:
            action = f"PERFORM pg_catalog.pg_advisory_xact_lock_shared({key});"
        psql(self.db, "-c", '''CREATE FUNCTION public.native_export_gate() RETURNS boolean
            LANGUAGE plpgsql VOLATILE AS $$ BEGIN
              IF current_setting('transaction_read_only') <> 'on'
                 OR current_setting('transaction_isolation') <> 'repeatable read' THEN
                RAISE EXCEPTION 'SENTINEL_TRANSACTION_SETTINGS';
              END IF;
              ''' + action + ''' RETURN true; END $$;
            ALTER TABLE public."LiteLLM_SpendLogs" ENABLE ROW LEVEL SECURITY;
            CREATE POLICY native_export_gate ON public."LiteLLM_SpendLogs"
              FOR SELECT TO fixture_reader USING (public.native_export_gate());''')

    @contextmanager
    def lock_gate(self, key):
        application = "native-lock-" + uuid.uuid4().hex
        command = ["docker", "exec", "-e", "PGAPPNAME=" + application, self.db,
                   "psql", "-Xq", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "fixture",
                   "-c", f"SELECT pg_advisory_lock({key}); SELECT pg_sleep(60);"]
        with background(command) as process:
            try:
                wait_for(self.db, f'''SELECT EXISTS (SELECT FROM pg_locks l
                    JOIN pg_stat_activity a USING (pid) WHERE a.application_name='{application}'
                    AND l.locktype='advisory' AND l.objid={key} AND l.granted)''',
                    "test advisory lock was not acquired", process)
                yield
            finally:
                scalar(self.db, f"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name='{application}'")

    @contextmanager
    def active_export(self, directory, **options):
        pid_file = self.parent + "/native.pid"
        with background(self.command(directory, pid_file=pid_file, **options)) as process:
            try:
                yield process, pid_file
            finally:
                if process.poll() is None:
                    raw = run(["docker", "exec", self.db, "cat", pid_file]).stdout.strip()
                    if raw.isdigit():
                        run(["docker", "exec", self.db, "sh", "-c",
                             'kill -s KILL "$1"', "fixture", raw.decode()])
                    scalar(self.db, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename='fixture_reader'")

    def wait_blocked(self, key, process):
        wait_for(self.db, f'''SELECT EXISTS (SELECT FROM pg_locks l
            JOIN pg_stat_activity a USING (pid) WHERE a.usename='fixture_reader'
            AND l.locktype='advisory' AND l.objid={key} AND NOT l.granted
            AND a.backend_xmin IS NOT NULL AND a.xact_start IS NOT NULL)''',
            "native export did not reach its snapshot lock", process)

    def test_repeatable_read_read_only_single_snapshot_during_concurrent_mutation(self):
        key = 730018
        for fmt in ("csv", "jsonl"):
            before_spend = spend.export_database(self.db)[fmt]
            before_daily = self.daily_oracle()["daily.csv"]
            self.install_gate(key)
            directory = self.destination()
            try:
                with self.lock_gate(key):
                    with self.active_export(directory, fmt=fmt, daily=True) as (process, _):
                        self.wait_blocked(key, process)
                        psql(self.db, data=b'''BEGIN;
                            UPDATE public."LiteLLM_SpendLogs" SET model=model || '-changed';
                            UPDATE public."LiteLLM_DailyUserSpend"
                              SET api_requests=api_requests+1, successful_requests=successful_requests+1;
                            COMMIT;''')
                        # Release only the blocker, leaving the export transaction intact.
                        scalar(self.db, f'''SELECT pg_terminate_backend(a.pid) FROM pg_stat_activity a
                            JOIN pg_locks l USING (pid) WHERE l.locktype='advisory'
                            AND l.objid={key} AND l.granted AND a.usename='postgres' ''')
                        self.check_result(finish(process), True)
                actual = self.read_export(directory, fmt, daily=True)
                assert_bytes(actual["spend." + fmt], before_spend, "snapshot spend")
                assert_bytes(actual["daily.csv"], before_daily, "snapshot daily")
            finally:
                psql(self.db, data=b'''DROP POLICY native_export_gate ON public."LiteLLM_SpendLogs";
                    ALTER TABLE public."LiteLLM_SpendLogs" DISABLE ROW LEVEL SECURITY;
                    DROP FUNCTION public.native_export_gate();''')
            require(spend.export_database(self.db)[fmt] != before_spend and
                    self.daily_oracle()["daily.csv"] != before_daily,
                    "concurrent control did not change both database projections")

    def test_read_only_role_and_server_error_privacy(self):
        require(scalar(self.db, '''SELECT has_table_privilege('fixture_reader',
            'public."LiteLLM_SpendLogs"', 'SELECT') AND NOT has_table_privilege('fixture_reader',
            'public."LiteLLM_SpendLogs"', 'INSERT,UPDATE,DELETE')''') == b"t",
            "fixture reader is not SELECT-only")
        oracle = spend.export_database(self.db)
        self.install_gate(730019)
        for fmt in ("csv", "jsonl"):
            actual = self.export(fmt=fmt)
            assert_bytes(actual["spend." + fmt], oracle[fmt], "read-only transaction spend")
        psql(self.db, "-c", """CREATE OR REPLACE FUNCTION public.native_export_gate()
            RETURNS boolean LANGUAGE plpgsql VOLATILE AS $$ BEGIN
              RAISE EXCEPTION 'SENTINEL_SERVER_DIAGNOSTIC'; END $$;""")
        for fmt in ("csv", "jsonl"):
            self.export(success=False, fmt=fmt)

    def test_interruption_never_publishes_complete_outputs(self):
        key = 730020
        self.install_gate(key)
        for signal in ("INT", "TERM"):
            for fmt in ("csv", "jsonl"):
                with self.subTest(signal=signal, format=fmt):
                    directory = self.destination()
                    with self.lock_gate(key):
                        with self.active_export(directory, fmt=fmt, daily=True) as (process, pid_file):
                            self.wait_blocked(key, process)
                            self.assert_no_output_directory(directory)
                            pid = docker("exec", self.db, "cat", pid_file).strip()
                            require(pid.isdigit(), "native process ID unavailable")
                            docker("exec", self.db, "sh", "-c",
                                   'kill -s "$1" "$2"', "fixture", signal, pid.decode())
                            self.check_result(finish(process), False)
                    self.assert_no_complete_outputs(directory)
                    self.assert_no_output_directory(directory)

    def test_actual_statement_timeout_never_publishes_complete_outputs(self):
        # Sleep occurs in the actual SELECT, not in the test runner or mock client.
        self.install_gate(730021, action="""IF current_setting('statement_timeout') <> '2min' THEN
            RAISE EXCEPTION 'SENTINEL_STATEMENT_TIMEOUT'; END IF;
            PERFORM pg_catalog.pg_sleep(180);""")
        directory = self.destination()
        with self.active_export(directory) as (process, _):
            wait_for(self.db, """SELECT EXISTS (SELECT FROM pg_stat_activity
                WHERE usename='fixture_reader' AND wait_event='PgSleep')""",
                "native export did not reach the statement-timeout control", process)
            # The observed PgSleep proves it got past schema/config validation.
            self.check_result(finish(process, timeout=150), False)
        self.assert_no_complete_outputs(directory)
        self.assert_no_output_directory(directory)


class HarnessPrivacyTests(unittest.TestCase):
    def test_byte_mismatch_does_not_print_either_row(self):
        try:
            assert_bytes(b"SENTINEL_PRIVATE_ROW_A", b"SENTINEL_PRIVATE_ROW_B", "spend")
        except AssertionError as error:
            require("SENTINEL" not in str(error), "byte assertion leaked row contents")
        else:
            self.fail("byte mismatch was accepted")


if __name__ == "__main__":
    unittest.main()
