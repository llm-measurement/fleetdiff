# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Synthetic session-budget CLI checks; no providers or database required."""
from collections import defaultdict
import csv
from decimal import Decimal
import json
import os
from pathlib import Path
import tempfile
import unittest

from test_rejections import encoded
import test_spend as spend


FIXTURE = spend.ROOT / "session-budget.csv"
PERIOD = "2026-10-01T00:00:00Z/2026-10-08T00:00:00Z"


def fixture_rows():
    with FIXTURE.open(newline="") as source:
        return list(csv.DictReader(source))


def review(path, records, *, output="json", hashes=False, success=True, top=10):
    command = [spend.CLI, "investigate", "--litellm-spend", str(path),
               "--group-by", "session", "--period", PERIOD,
               "--session-budget-usd", "1.00", "--format", output,
               "--hash-secret-env", "FLEETDIFF_TEST_SECRET", "--top", str(top)]
    if hashes:
        command.append("--show-hashes")
    result = spend.run(command, env=dict(os.environ, FLEETDIFF_TEST_SECRET=spend.SECRET))
    spend.assert_private(result, (path, path.parent), records)
    spend.require((result.returncode == 0) == success, "unexpected session-review exit status")
    if not success:
        spend.require(not result.stdout and bool(result.stderr), "session error was not atomic")
    elif output == "json":
        try:
            return json.loads(result.stdout)
        except (ValueError, UnicodeError):
            raise AssertionError("session review did not return valid JSON") from None
    return result


class SessionFixtureTests(unittest.TestCase):
    def test_fresh_fixture_arithmetic_and_license(self):
        records = fixture_rows()
        self.assertEqual(len(records), 13)
        self.assertEqual(len({row["request_id"] for row in records}), 13)
        totals, calls = defaultdict(Decimal), defaultdict(int)
        for row in records:
            if not row["rejection_kind"]:
                totals[row["session_id"]] += Decimal(row["spend"])
                calls[row["session_id"]] += 1
        self.assertEqual(sorted(totals.values()), list(map(Decimal, ("0.20", "1.10", "1.20", "1.60"))))
        self.assertEqual(sorted(calls.values()), [1, 2, 3, 6])
        self.assertEqual(sum(bool(row["rejection_kind"]) for row in records), 1)
        self.assertIn("SPDX-License-Identifier: Apache-2.0",
                      FIXTURE.with_suffix(".csv.license").read_text())


@unittest.skipUnless(spend.CLI, "set FLEETDIFF_BIN to the integrated session-budget CLI")
class SessionBudgetTests(unittest.TestCase):
    def test_complete_sessions_cap_shared_identities_and_rejection(self):
        records = fixture_rows()
        original = FIXTURE.read_bytes()
        reports = {}
        for hashes in (False, True):
            report = review(FIXTURE, records, hashes=hashes)
            reports[hashes] = report
            self.assertEqual(report["grouping"], "session_id_only")
            self.assertEqual(report["period"], {"start": "2026-10-01T00:00:00Z",
                                              "end_exclusive": "2026-10-08T00:00:00Z"})
            self.assertEqual(report["sessions_with_model_calls"], 4)
            self.assertEqual(report["complete_sessions_exceeding_cap"], 3)
            self.assertEqual(report["partial_subtotals_exceeding_cap"], 0)
            self.assertEqual(report["exceeding_without_recorded_session_budget_rejection"], 2)
            self.assertEqual(report["complete_recorded_spend_usd"],
                             {"n": 4, "p50": "1.1", "p95": "1.6", "max": "1.6"})
            self.assertEqual(report["logged_model_calls"],
                             {"n": 4, "p50": "2", "p95": "6", "max": "6"})
            self.assertEqual(report["logged_model_calls_total"], 12)
            self.assertEqual(report["recorded_tokens"], 1320)
            self.assertEqual(report["rejection_classified_rows"], 13)
            self.assertEqual(report["recorded_rejections"], {"session_budget": 1})
            self.assertEqual(report["sessions_with_recorded_budget_or_iteration_rejection"], 1)
            self.assertEqual(report["singleton_sessions"], 1)
            self.assertEqual(report["sessions_under_multiple_identities"], 1)
            shared = report["shared_counter_review"][0]
            self.assertEqual((shared["keys"], shared["teams"], shared["agents"]), (2, 2, 2))
            self.assertEqual(shared["recorded_spend_usd"], "1.6")
            self.assertEqual(report["sessions_spanning_counter_window"], 1)
            self.assertEqual(report["counter_window_review"][0]["positive_spend_span_seconds"], 4000)
            cap = {row["recorded_spend_usd"]: row for row in report["cap_review"]}
            self.assertEqual(set(cap), {"1.1", "1.2", "1.6"})
            self.assertEqual(cap["1.1"]["recorded_rejections"], {"session_budget": 1})
            self.assertEqual(cap["1.1"]["logged_model_calls"], 6)
            self.assertTrue(cap["1.1"]["recorded_budget_or_iteration_rejection"])
            for total in ("1.2", "1.6"):
                self.assertEqual(cap[total]["recorded_rejections"], {})
                self.assertFalse(cap[total]["recorded_budget_or_iteration_rejection"])
            for row in report["largest_sessions"]:
                self.assertEqual("hash" in row, hashes)
                if hashes:
                    self.assertRegex(row["hash"], r"^[0-9a-f]{64}$")
            text = review(FIXTURE, records, output="text", hashes=hashes).stdout.decode()
            self.assertTrue(text.startswith(
                "3 of 4 sessions with complete recorded spend exceed the supplied USD 1.00 cap."))
            self.assertIn("Above-cap sessions with no recorded session-budget rejection: 2.", text)
            self.assertIn("keys=2, teams=2, agents=2.", text)
            self.assertIn("session_budget recorded rejections: 1.", text)
            for calls in (1, 2, 3, 6):
                self.assertIn(f"; calls={calls}", text)
            self.assertEqual("; hash " in text, hashes)
        with tempfile.TemporaryDirectory(dir="/tmp") as temp:
            for fmt in ("json", "jsonl"):
                path = Path(temp) / ("session." + fmt)
                path.write_bytes(encoded(records, fmt))
                for hashes in (False, True):
                    self.assertEqual(review(path, records, hashes=hashes), reports[hashes])
        spend.require(FIXTURE.read_bytes() == original, "session fixture changed during import")

    def test_missing_spend_is_separate_from_complete_population(self):
        records = fixture_rows()
        records[-1]["spend"] = ""
        with tempfile.TemporaryDirectory(dir="/tmp") as temp:
            path = Path(temp) / "missing-spend.csv"
            path.write_bytes(encoded(records, "csv"))
            report = review(path, records)
            self.assertEqual(report["sessions_with_model_calls"], 4)
            self.assertEqual(report["complete_recorded_spend_usd"],
                             {"n": 3, "p50": "1.1", "p95": "1.2", "max": "1.2"})
            self.assertEqual(report["logged_model_calls"]["n"], 4)
            self.assertEqual(report["complete_sessions_exceeding_cap"], 2)
            self.assertEqual(report["partial_subtotals_exceeding_cap"], 1)
            self.assertEqual(report["exceeding_without_recorded_session_budget_rejection"], 1)
            self.assertEqual(report["usage_quality"]["missing_recorded_spend"], 1)
            partial = [row for row in report["cap_review"] if not row["spend_complete"]]
            self.assertEqual(len(partial), 1)
            self.assertEqual(partial[0]["recorded_spend_usd"], "1.2")
            self.assertEqual(partial[0]["missing_spend_rows"], 1)
            text = review(path, records, output="text").stdout.decode()
            self.assertIn("2 of 3 sessions with complete recorded spend", text)
            self.assertIn("1 incomplete spend subtotals already exceed the cap", text)

    def test_old_columns_preserve_totals_with_unknown_rejection_coverage(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as temp:
            for omit_agent in (False, True):
                excluded = {"rejection_kind", "agent_id"} if omit_agent else {"rejection_kind"}
                records = [{key: value for key, value in row.items() if key not in excluded}
                           for row in fixture_rows()]
                path = Path(temp) / "old-columns.csv"
                path.write_bytes(encoded(records, "csv"))
                report = review(path, records)
                self.assertEqual(report["complete_sessions_exceeding_cap"], 3)
                self.assertEqual(report["complete_recorded_spend_usd"]["n"], 4)
                self.assertIsNone(report["exceeding_without_recorded_session_budget_rejection"])
                self.assertEqual(report["rejection_classified_rows"], 0)
                self.assertEqual(report["recorded_rejections"], {})
                self.assertTrue(all(not row["rejection_classification_complete"]
                                    for row in report["cap_review"]))
                self.assertEqual(report["shared_counter_review"][0]["agents"], 0 if omit_agent else 2)
                # Without the classifier the historical failed zero row is not a known pre-call rejection.
                self.assertEqual(report["logged_model_calls_total"], 13)
                text = review(path, records, output="text").stdout.decode()
                self.assertIn("Rejection coverage is unknown", text)
                self.assertNotIn("Above-cap sessions with no recorded session-budget rejection:", text)

    def test_top_does_not_change_counts_or_distributions(self):
        records = fixture_rows()
        full = review(FIXTURE, records)
        limited = review(FIXTURE, records, top=1)
        lists = {"largest_sessions", "cap_review", "counter_window_review", "shared_counter_review"}
        self.assertEqual({k: v for k, v in full.items() if k not in lists},
                         {k: v for k, v in limited.items() if k not in lists})
        self.assertTrue(all(len(limited[key]) <= 1 for key in lists))

    def test_agent_sentinels_never_escape_errors_with_or_without_hashes(self):
        with tempfile.TemporaryDirectory(prefix="SENTINEL_PATH_session-", dir="/tmp") as temp:
            for error in ("enum", "status", "timestamp", "duplicate"):
                records = fixture_rows()
                agent = records[0]["agent_id"]
                if error == "enum":
                    records[0]["rejection_kind"] = agent
                elif error == "status":
                    records[0]["rejection_kind"] = "session_budget"
                elif error == "timestamp":
                    records[0]["startTime"] = agent
                else:
                    records[0]["request_id"] = records[1]["request_id"] = agent
                path = Path(temp) / (error + ".csv")
                path.write_bytes(encoded(records, "csv"))
                for output in ("text", "json"):
                    for hashes in (False, True):
                        with self.subTest(error=error, output=output, hashes=hashes):
                            review(path, records, output=output, hashes=hashes, success=False)


if __name__ == "__main__":
    unittest.main()
