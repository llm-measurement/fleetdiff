# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Opt-in routing projection: native writer, offline PostgreSQL and old readers."""
import copy
import csv
import io
import json
import os
from pathlib import Path
import tempfile
import unittest

import test_spend as spend
from test_rejections import encoded


ROUTING_COLUMNS = ("original_model_group", "attempted_fallbacks", "internal_call_origin")
RELEASES = {"v0.7.0": os.environ.get("FLEETDIFF_V070_BIN"),
            "v0.10.1": os.environ.get("FLEETDIFF_V0101_BIN")}


def routing_rows():
    records = list(spend.rows())
    for index, row in enumerate(records):
        values = (("SENTINEL_ORIGINAL_ROUTE", 0, None),
                  ("SENTINEL_ORIGINAL_ROUTE", 1, "autorouter_classifier"),
                  (None, None, None), ("", "", ""))[index % 4]
        row.update(zip(ROUTING_COLUMNS, values))
    return records


def decoded(blob, fmt):
    records = spend.decode(blob, fmt)
    for row in records:
        for name in ROUTING_COLUMNS:
            if name in row and row[name] is None:
                row[name] = ""
            elif name == "attempted_fallbacks" and name in row:
                row[name] = str(row[name])
    return records


def assert_compatible(binary, exports, version=None):
    if version:
        result = spend.run([binary, "--version"])
        spend.require(result.returncode == 0 and
                      result.stdout.startswith(("fleetdiff " + version + " (").encode()),
                      "routing compatibility requires the specified released binary")
    env = dict(os.environ, FLEETDIFF_TEST_SECRET=spend.SECRET)
    with tempfile.TemporaryDirectory() as temp:
        for fmt, blob in exports.items():
            records = spend.decode(blob, fmt)
            base = [{k: v for k, v in row.items() if k not in ROUTING_COLUMNS} for row in records]
            old = Path(temp) / ("baseline." + fmt)
            new = Path(temp) / ("routing." + fmt)
            old.write_bytes(encoded(base, fmt))
            for group in spend.GROUPS:
                for hashes in (False, True):
                    for output in ("text", "json"):
                        args = ["investigate", "--before-period", "2026-10-07",
                                "--after-period", "2026-10-08", "--group-by", group,
                                "--hash-secret-env", "FLEETDIFF_TEST_SECRET", "--format", output]
                        if hashes:
                            args.append("--show-hashes")
                        before = spend.run([binary, *args, "--litellm-spend", str(old)], env=env)
                        spend.require(before.returncode == 0, "routing baseline reader failed")
                        spend.assert_private(before, (old, new), records)
                        for columns in (*((name,) for name in ROUTING_COLUMNS), ROUTING_COLUMNS):
                            selected = [{k: v for k, v in row.items()
                                         if k not in ROUTING_COLUMNS or k in columns} for row in records]
                            new.write_bytes(encoded(selected, fmt))
                            after = spend.run([binary, *args, "--litellm-spend", str(new)], env=env)
                            spend.assert_private(after, (old, new), records)
                            spend.require((before.returncode, before.stdout, before.stderr) ==
                                          (after.returncode, after.stdout, after.stderr),
                                          "reader output changed for an additive routing column")


class RoutingFixtureTests(unittest.TestCase):
    def test_default_off_exact_paths_and_no_numeric_cast(self):
        sql = (spend.ROOT / "export.sql").read_text()
        spend.require("\\set routing_details false" in sql, "routing projection is not opt-in")
        fragment = sql.split("THEN $routing$", 1)[1].split("$routing$ ELSE", 1)[0]
        for name in ROUTING_COLUMNS:
            spend.require("metadata->>'" + name + "'" in fragment and "AS " + name in fragment,
                          "routing projection path or alias changed")
        spend.require(fragment.count(" AS ") == 3 and "::" not in fragment,
                      "routing projection added a field or numeric cast")
        spend.require("routing_decision" not in fragment and "litellm_call_id" not in fragment,
                      "routing projection exports unnecessary private metadata")

    def test_fresh_synthetic_format_parity(self):
        records = routing_rows()
        canonical = decoded(encoded(records, "csv"), "csv")
        for fmt in spend.FORMATS:
            spend.require(decoded(encoded(records, fmt), fmt) == canonical,
                          "routing synthetic format parity failed")

    @unittest.skipUnless(spend.CLI, "set FLEETDIFF_BIN for current-reader routing compatibility")
    def test_current_default_output_unchanged(self):
        assert_compatible(spend.CLI, {fmt: encoded(routing_rows(), fmt) for fmt in spend.FORMATS})

    @unittest.skipUnless(RELEASES["v0.7.0"], "set FLEETDIFF_V070_BIN for v0.7.0 routing compatibility")
    def test_v070_each_column_and_combination(self):
        assert_compatible(RELEASES["v0.7.0"],
                          {fmt: encoded(routing_rows(), fmt) for fmt in spend.FORMATS}, "v0.7.0")

    @unittest.skipUnless(RELEASES["v0.10.1"], "set FLEETDIFF_V0101_BIN for v0.10.1 routing compatibility")
    def test_v0101_each_column_and_combination(self):
        assert_compatible(RELEASES["v0.10.1"],
                          {fmt: encoded(routing_rows(), fmt) for fmt in spend.FORMATS}, "v0.10.1")


@unittest.skipUnless(spend.DOCKER, "set FLEETDIFF_SPEND_DOCKER=1 for pinned routing DB tests")
class RoutingDatabaseTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.payloads = spend.writer_payloads("--routing")
        cls.records = list(cls.payloads)
        # These test SQL shape guards only, not runtime writer behavior.
        for index, metadata in enumerate((None, [], "SENTINEL_METADATA", {},
                {name: None for name in ROUTING_COLUMNS},
                {name: "" for name in ROUTING_COLUMNS},
                {name: {"private": "SENTINEL_NESTED"} for name in ROUTING_COLUMNS},
                {name: ["SENTINEL_ARRAY"] for name in ROUTING_COLUMNS},
                {name: True for name in ROUTING_COLUMNS},
                {"original_model_group": 123, "internal_call_origin": 123,
                 "attempted_fallbacks": "SENTINEL_INVALID_COUNT"},
                {"attempted_fallbacks": 9223372036854775808},
                {"attempted_fallbacks": -1}, {"attempted_fallbacks": 1.25})):
            row = copy.deepcopy(cls.payloads[0])
            row.update(request_id=f"synthetic-routing-mutation-{index}", metadata=json.dumps(metadata))
            cls.records.append(row)
        cls.context = spend.database()
        cls.db = cls.context.__enter__()
        cls.addClassCleanup(cls.context.__exit__, None, None, None)
        spend.seed_database(cls.db, cls.records)
        cls.exports = {(details, routing): spend.export_database(cls.db, details=details, routing=routing)
                       for details in (None, False, True) for routing in (None, False, True)}
        output = os.environ.get("FLEETDIFF_ROUTING_OUTPUT")
        if output:
            destination = Path(output)
            destination.mkdir(mode=0o700, parents=False, exist_ok=False)
            evidence = {"writer-payloads.json": json.dumps(cls.payloads).encode(),
                        "database-inspect.json": spend.docker("inspect", cls.db)}
            for (details, routing), exports in cls.exports.items():
                for fmt, blob in exports.items():
                    evidence[f"details-{details}-routing-{routing}.{fmt}"] = blob
            for name, blob in evidence.items():
                with os.fdopen(os.open(destination / name, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "wb") as handle:
                    handle.write(blob)

    def test_writer_paths_and_native_usage(self):
        spend.require(len(self.payloads) == 16, "routing writer omitted a case")
        for row in self.payloads:
            metadata = json.loads(row["metadata"])
            case = row["request_id"].rsplit("-", 1)[-1]
            if case in ("ordinary", "recovery", "classifier", "responses"):
                spend.require(metadata["original_model_group"] == "SENTINEL_ORIGINAL_ROUTE",
                              "writer original route changed")
                spend.require(metadata["attempted_fallbacks"] == (1 if case == "recovery" else 0),
                              "writer fallback counter changed")
            if case == "recovery":
                spend.require(row["model_group"] != metadata["original_model_group"],
                              "recovery group incorrectly used as original route")
            if case == "classifier":
                spend.require(row["call_type"] == "pass_through_endpoint" and
                              metadata["internal_call_origin"] == "autorouter_classifier",
                              "writer classifier marker changed")
            if case == "responses":
                spend.require(row["agent_id"] is None and
                              [row[k] for k in ("prompt_tokens", "completion_tokens", "total_tokens")] == [200, 10, 210],
                              "native Responses usage or supplied missing agent changed")
            if case == "interrupted":
                spend.require(metadata["original_model_group"] is None and
                              metadata["attempted_fallbacks"] is None and row["prompt_tokens"] == 14,
                              "missing routing metadata was inferred from failure usage")

    def test_default_bytes_and_base_arithmetic_unchanged(self):
        for details in (None, False, True):
            base = self.exports[details, None]
            spend.require(base == self.exports[details, False], "routing false changed default export bytes")
            for fmt in spend.FORMATS:
                original = decoded(base[fmt], fmt)
                full = decoded(self.exports[details, True][fmt], fmt)
                stripped = [{k: v for k, v in row.items() if k not in ROUTING_COLUMNS} for row in full]
                spend.require(stripped == original, "routing changed base columns, counts or sums")
                spend.require(all(not set(row).intersection(ROUTING_COLUMNS) for row in original),
                              "routing columns leaked into default export")
                spend.require(all(set(row).difference(original[i]) == set(ROUTING_COLUMNS)
                                  for i, row in enumerate(full)), "routing projection column set changed")
        spend.require(self.exports[None, True] == self.exports[True, True],
                      "explicit default usage details changed routing export")

    def test_sql_shapes_nulls_exact_aliases_and_privacy(self):
        originals = {row["request_id"]: row for row in self.records}
        for details in (None, False, True):
            exports = self.exports[details, True]
            canonical = decoded(exports["csv"], "csv")
            for fmt in spend.FORMATS:
                spend.require(decoded(exports[fmt], fmt) == canonical, "routing SQL format parity failed")
                for marker in (b"SENTINEL_PROMPT", b"SENTINEL_RESPONSE", b"SENTINEL_CODE",
                               b"SENTINEL_PATH", b"SENTINEL_NESTED", b"SENTINEL_ARRAY", b"SENTINEL_METADATA"):
                    spend.require(marker not in exports[fmt], "SQL leaked ignored private content")
            for row in (json.loads(line) for line in exports["jsonl"].splitlines()):
                raw = originals[row["request_id"]]["metadata"]
                metadata = json.loads(raw) if isinstance(raw, str) else raw
                for name in ROUTING_COLUMNS:
                    value = metadata.get(name) if isinstance(metadata, dict) else None
                    expected = value if isinstance(value, str) else None
                    if name == "attempted_fallbacks" and type(value) in (int, float):
                        expected = str(value)
                    spend.require(row[name] == expected, "SQL routing alias or NULL contract changed")
            raw_header = next(csv.reader(io.StringIO(exports["csv"].decode())))
            spend.require(tuple(raw_header[-3:]) == ROUTING_COLUMNS, "routing column order changed")

    def test_empty_interval_is_valid_with_routing(self):
        exports = spend.export_database(self.db, start="2026-10-10T00:00:00Z",
                                        end="2026-10-11T00:00:00Z", routing=True)
        spend.require(exports["jsonl"] == b"" and json.loads(exports["json"]) == [],
                      "empty routing export is not empty")
        spend.require(tuple(next(csv.reader(io.StringIO(exports["csv"].decode())))[-3:]) == ROUTING_COLUMNS,
                      "empty routing CSV omitted its header")

    @unittest.skipUnless(RELEASES["v0.7.0"], "set FLEETDIFF_V070_BIN for v0.7.0 routing DB compatibility")
    def test_v070_actual_db_columns_and_cache_opt_out(self):
        for details in (False, True):
            assert_compatible(RELEASES["v0.7.0"], self.exports[details, True], "v0.7.0")

    @unittest.skipUnless(RELEASES["v0.10.1"], "set FLEETDIFF_V0101_BIN for v0.10.1 routing DB compatibility")
    def test_v0101_actual_db_columns_and_cache_opt_out(self):
        for details in (False, True):
            assert_compatible(RELEASES["v0.10.1"], self.exports[details, True], "v0.10.1")


if __name__ == "__main__":
    unittest.main()
