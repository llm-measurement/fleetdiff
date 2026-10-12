# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Pinned rejection projection and released-reader compatibility, entirely local."""
import copy
import csv
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import test_spend as spend


RELEASE = os.environ.get("FLEETDIFF_V070_BIN")
KINDS = ("session_budget", "session_iterations", "other_budget")


def encoded(records, fmt):
    if fmt == "csv":
        output = io.StringIO()
        writer = csv.DictWriter(output, fieldnames=list(records[0]), lineterminator="\n")
        writer.writeheader()
        writer.writerows(records)
        return output.getvalue().encode()
    if fmt == "json":
        return json.dumps(records).encode()
    return ("\n".join(json.dumps(row) for row in records) + "\n").encode()


def assert_released_reader(exports):
    version = spend.run([RELEASE, "--version"])
    spend.require(version.returncode == 0 and version.stdout.startswith(b"fleetdiff v0.7.0 ("),
                  "compatibility check requires the released v0.7.0 binary")
    with tempfile.TemporaryDirectory(dir="/tmp") as temp, mock.patch.object(spend, "CLI", RELEASE):
        for fmt, blob in exports.items():
            records = spend.decode(blob, fmt)
            legacy = [{k: v for k, v in row.items() if k not in ("agent_id", "rejection_kind")}
                      for row in records]
            new_path, old_path = Path(temp) / ("new." + fmt), Path(temp) / ("old." + fmt)
            new_path.write_bytes(blob)
            old_path.write_bytes(encoded(legacy, fmt))
            for group in spend.GROUPS:
                for hashes in (False, True):
                    for output in ("json", "text"):
                        new = spend.investigate(new_path, group=group, hashes=hashes,
                                                encoding=output, records=records)
                        old = spend.investigate(old_path, group=group, hashes=hashes,
                                                encoding=output, records=records)
                        spend.require(new == old if output == "json" else
                                      (new.stdout, new.stderr) == (old.stdout, old.stderr),
                                      "released reader changed output for additive columns")


@unittest.skipUnless(RELEASE, "set FLEETDIFF_V070_BIN for released-reader compatibility")
class ReleasedReaderTests(unittest.TestCase):
    def test_synthetic_added_columns_are_ignored_by_v070(self):
        records = [dict(row, agent_id="SENTINEL_AGENT_73b9",
                        rejection_kind=KINDS[index % 3] if row["status"] == "failure" else None)
                   for index, row in enumerate(spend.rows())]
        assert_released_reader({fmt: encoded(records, fmt) for fmt in spend.FORMATS})


@unittest.skipUnless(spend.DOCKER, "set FLEETDIFF_SPEND_DOCKER=1 for pinned rejection DB tests")
class RejectionDatabaseTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        positives = spend.writer_payloads("--rejections")
        cls.expected = {"synthetic-rejection-" + kind: kind for kind in KINDS}
        cls.records = list(positives)
        # Change each required discriminator independently for all three shapes.
        for original in positives:
            metadata = json.loads(original["metadata"])
            info = metadata["error_information"]
            mutations = {
                "code": {"error_code": "500"},
                "class": {"error_class": "RateLimitError"},
                "category": {"error_rate_limit_category": "provider_rate_limit"},
                "type": {"error_rate_limit_type": "rpm"},
                "conflicting-type": {"error_rate_limit_type": "budget" if
                                     info["error_rate_limit_type"] == "max_iterations" else "max_iterations"},
                "prefix": {"error_message": "Custom SENTINEL_ERROR_73b9"},
                "embedded-prefix": {"error_message": "wrapped: " + info["error_message"]},
                "missing-message": {"error_message": None},
                "success": {}, "missing-status": {},
                "missing-metadata": {}, "empty-metadata": {}, "nonobject-metadata": {},
                "missing-information": {}, "normalized-only": {},
            }
            if cls.expected[original["request_id"]] == "other_budget":
                mutations["normalized-code-429"] = {"error_code": "429"}
            for name, changes in mutations.items():
                row = copy.deepcopy(original)
                row["request_id"] += "-" + name
                row["metadata"] = copy.deepcopy(metadata)
                row["metadata"]["error_information"].update(changes)
                if name in ("success", "missing-status"):
                    row["status"] = "success" if name == "success" else None
                replacements = {"missing-metadata": None, "empty-metadata": {},
                                "nonobject-metadata": [], "missing-information": {"error_information": {}},
                                "normalized-only": {"error_information": {"normalized_error": "429_BUDGET_EXCEEDED"}}}
                if name in replacements:
                    row["metadata"] = replacements[name]
                cls.records.append(row)
                cls.expected[row["request_id"]] = ""
        # Legacy successful model rows provide both periods for released-reader checks.
        cls.records.extend(dict(row, metadata={}, messages=None, response=None,
                                proxy_server_request=None) for row in spend.rows())
        cls.context = spend.database()
        cls.db = cls.context.__enter__()
        cls.addClassCleanup(cls.context.__exit__, None, None, None)
        spend.seed_database(cls.db, cls.records)
        cls.exports = spend.export_database(cls.db)
        cls.plain = spend.export_database(cls.db, details=False)

    def test_exact_classes_negative_cases_nulls_and_agent(self):
        for exports in (self.exports, self.plain):
            for fmt in spend.FORMATS:
                rows = spend.decode(exports[fmt], fmt)
                actual = {row["request_id"]: row["rejection_kind"] for row in rows
                          if row["request_id"] in self.expected}
                spend.require(actual == self.expected, "pinned rejection classification differs")
                spend.require(all(row["agent_id"] == "SENTINEL_AGENT_73b9" for row in rows
                                  if row["request_id"] in self.expected), "agent identity lost in export")
                spend.require(all(row["rejection_kind"] == "" for row in rows
                                  if row["request_id"] not in self.expected), "ordinary row classified")
                for sentinel in (b"SENTINEL_ERROR_", b"SENTINEL_PROMPT_", b"SENTINEL_RESPONSE_",
                                 b"SENTINEL_CODE_", b"SENTINEL_PATH_", b"error_message", b"traceback"):
                    spend.require(sentinel not in exports[fmt], "SQL exported private error/content data")
            raw = [json.loads(line) for line in exports["jsonl"].splitlines()]
            spend.require(all(row["rejection_kind"] is None for row in raw
                              if not self.expected.get(row["request_id"])), "unknown is not JSON null")
        full = {row["request_id"]: row for row in spend.decode(self.exports["csv"], "csv")}
        for row in spend.decode(self.plain["csv"], "csv"):
            spend.require(row == {key: value for key, value in full[row["request_id"]].items()
                                  if key not in spend.DETAIL_PATHS}, "opt-out changed base projection")

    @unittest.skipUnless(RELEASE, "set FLEETDIFF_V070_BIN for DB-export compatibility")
    def test_released_reader_accepts_actual_database_export(self):
        assert_released_reader(self.exports)
        assert_released_reader(self.plain)


if __name__ == "__main__":
    unittest.main()
