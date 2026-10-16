"""Offline tests for the public scalar projection and shipped data contract."""

import copy
import csv
from decimal import Decimal
import importlib.util
import io
import json
from pathlib import Path
import re
import unittest
from unittest.mock import patch
from urllib.parse import urlparse

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("reproduce", ROOT / "scripts/reproduce-model-mix.py")
REPRO = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(REPRO)


class PublicModelMixTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.rows = REPRO.load_counts()
        cls.factors, cls.diagnostics = REPRO.reproduce(cls.rows)
        cls.source = REPRO.read_json(REPRO.EVIDENCE / "source-record.json")
        cls.prices = REPRO.read_json(REPRO.DATA / "prices.json")

    def invalid_csv(self, rows, fields=REPRO.COLUMNS):
        stream = io.StringIO()
        writer = csv.DictWriter(stream, fieldnames=fields, lineterminator="\n")
        writer.writeheader()
        writer.writerows(rows)
        with patch.object(Path, "open", return_value=io.StringIO(stream.getvalue())):
            with self.assertRaises(ValueError):
                REPRO.load_counts()

    def test_reproduction_and_manifest(self):
        REPRO.check_public()

    def test_original_full_precision(self):
        self.assertEqual(self.source["original-fit"]["estimates"], self.diagnostics["strata"])
        self.assertEqual(self.source["original-fit"]["decimal_precision"], 50)
        self.assertIn("geometric", self.source["original-fit"]["interval_target"])
        self.assertEqual(self.source["original-fit"]["estimator"], REPRO.METHOD)

    def test_tampered_shipped_factor_refused(self):
        original_reader = REPRO.read_json
        bad = copy.deepcopy(self.factors)
        bad["pairs"][0]["output"] = "1"
        def altered(path):
            return bad if path == REPRO.DATA / "factors.json" else original_reader(path)
        with patch.object(REPRO, "read_json", side_effect=altered), self.assertRaises(ValueError):
            REPRO.check_public()

    def test_geometric_not_arithmetic_or_ratio_of_sums(self):
        pairs = [(1, 1)] * 20 + [(1, 4)] * 19
        expected = 4 ** (19 / 39)
        actual = float(REPRO.estimate(pairs)["factor"])
        self.assertAlmostEqual(actual, expected, places=14)
        self.assertGreater(abs(actual - sum(b for _, b in pairs) / 39), 0.1)

    def test_original_normal_interval(self):
        import math
        import statistics
        pairs = [(10 + i, 30 + i * 2) for i in range(39)]
        logs = [math.log(b / a) for a, b in pairs]
        mean = statistics.mean(logs)
        margin = REPRO.Z95 * statistics.stdev(logs) / math.sqrt(39)
        value = REPRO.estimate(pairs)
        self.assertAlmostEqual(float(value["low"]), math.exp(mean - margin), places=14)
        self.assertAlmostEqual(float(value["high"]), math.exp(mean + margin), places=14)

    def test_undefined_ratios_refused(self):
        for bad in ((0, 2), (2, 0), (-1, 2), (True, 2)):
            with self.subTest(bad=bad), self.assertRaises(ValueError):
                REPRO.estimate([(1, 2)] * 38 + [bad])

    def test_fit_size_not_silently_changed(self):
        for n in (0, 1, 38, 40, 78, 116, 118):
            with self.subTest(n=n), self.assertRaises(ValueError):
                REPRO.estimate([(1, 2)] * n)

    def test_no_inferred_directions(self):
        actual = {(r["from"], r["to"]) for r in self.factors["input_pairs"]}
        self.assertEqual(actual, set(REPRO.DIRECTIONS))
        self.assertFalse(actual & {(b, a) for a, b in actual})
        expected = {(a, b, w) for a, b in actual for w in REPRO.WORKLOADS}
        self.assertEqual({(r["from"], r["to"], r["workload"]) for r in self.factors["pairs"]}, expected)

    def test_factor_schema_and_precision(self):
        self.assertEqual(set(self.factors), {"version", "date", "source_record", "method", "input_pairs", "pairs"})
        for row in self.factors["input_pairs"]:
            self.assertEqual(set(row), {"from", "to", "factor", "n", "source_record"})
            self.assertEqual(row["n"], 117)
        for row in self.factors["pairs"]:
            self.assertEqual(set(row), {"from", "to", "workload", "output", "output_low", "output_high", "n", "source_record"})
            self.assertEqual(row["n"], 39)
            self.assertLessEqual(Decimal(row["output_low"]), Decimal(row["output"]))
            self.assertLessEqual(Decimal(row["output"]), Decimal(row["output_high"]))
        for row in self.factors["input_pairs"] + self.factors["pairs"]:
            for key in ("factor", "output", "output_low", "output_high"):
                if key in row:
                    self.assertGreater(REPRO.decimal_string(row[key]), 0)
                    self.assertLessEqual(len(row[key]), 48)
                    self.assertLessEqual(len(row[key].partition(".")[2]), 18)

    def test_round_half_even(self):
        self.assertEqual(REPRO.shipped_decimal("1.1234567890123456785"), "1.123456789012345678")
        self.assertEqual(REPRO.shipped_decimal("1.1234567890123456795"), "1.12345678901234568")
        self.assertEqual(REPRO.shipped_decimal("1"), "1")

    def test_common_input_not_workload_input(self):
        inputs = self.factors["input_pairs"]
        for row in inputs:
            self.assertNotIn("workload", row)
            if REPRO.PROVIDERS[row["from"]] == REPRO.PROVIDERS[row["to"]]:
                self.assertEqual(row["factor"], "1")
            else:
                self.assertEqual(row["factor"], "0.668804954773279205")
        self.assertTrue(all("input" not in r for r in self.factors["pairs"]))

    def test_csv_rejects_duplicate_missing_unknown_nonfit_and_zero(self):
        self.invalid_csv(self.rows[:-1])
        variants = ({"model": "unmeasured"}, {"workload": "other"},
                    {"split_index": 39}, {"input_tokens": 0},
                    {"output_tokens": 4097}, {"task_id": "untrusted-text"})
        for change in variants:
            with self.subTest(change=change):
                rows = copy.deepcopy(self.rows)
                rows[0].update(change)
                self.invalid_csv(rows)
        rows = copy.deepcopy(self.rows)
        rows[1] = rows[0]
        self.invalid_csv(rows)

    def test_csv_rejects_extra_text_column(self):
        rows = [dict(row, extra="unreleased") for row in self.rows]
        self.invalid_csv(rows, (*REPRO.COLUMNS, "extra"))

    def test_json_rejects_duplicates_nonfinite_and_numeric_rates(self):
        for value in ('{"x":1,"x":2}', '{"x":NaN}', '{"x":Infinity}'):
            with patch.object(Path, "read_text", return_value=value), self.assertRaises(ValueError):
                REPRO.read_json(Path("unused"))
        for value in (1, 0.1, True, "NaN", "1e3", "-1", ".1"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                REPRO.decimal_string(value)

    def test_prices_exact_literal_strings_and_tiers(self):
        self.assertEqual(set(self.prices), {"version", "date", "source_record", "models", "currency", "unit", "tier"})
        self.assertEqual(self.prices["date"], "2026-10-14")
        for model in self.prices["models"]:
            fields = {"id", "provider", "input", "output", "cache_read", "cache_write", "source_url", "cache_write_ttl"}
            if model["id"] != "claude-sonnet-5-5":
                fields.add("input_tier_max")
            self.assertEqual(set(model), fields)
            expected = ("2", "10", "0.10", "2.50") if model["id"] in REPRO.MODELS[:2] else ("0.10", "0.50", "0.01", "0.125")
            self.assertEqual(tuple(model[k] for k in ("input", "output", "cache_read", "cache_write")), expected)
            if "input_tier_max" in model:
                self.assertIs(type(model["input_tier_max"]), int)
                self.assertEqual(model["input_tier_max"], 100000 if model["provider"] == "anthropic" else 272000)
            self.assertEqual(model["cache_write_ttl"], "5m" if model["provider"] == "anthropic" else "30m minimum retention")

    def test_public_source_references(self):
        refs = [self.factors["source_record"], self.prices["source_record"]]
        refs += [r["source_record"] for r in self.factors["input_pairs"] + self.factors["pairs"]]
        refs += [r["source_url"] for r in self.prices["models"]]
        for value in refs:
            parsed = urlparse(value)
            self.assertEqual(parsed.scheme, "https")
            self.assertIn(parsed.hostname, {"raw.githubusercontent.com", "developers.openai.com", "platform.claude.com"})
            self.assertIsNone(parsed.username)
            self.assertFalse(parsed.query)

    def test_license_pins_and_negative_validation_retained(self):
        datasets = self.source["datasets"]
        self.assertEqual([d["license"] for d in datasets], ["CC-BY-SA-3.0", "MIT"])
        for dataset in datasets:
            self.assertRegex(dataset["revision"], r"^[a-f0-9]{40}$")
            self.assertRegex(dataset["sha256"], r"^[a-f0-9]{64}$")
            self.assertRegex(dataset["license_sha256"], r"^[a-f0-9]{64}$")
        validation = self.source["audit"]["original_validation"]
        self.assertEqual(validation["workload|per_task"]["joint_pass"], 0)
        self.assertEqual(validation["pooled|per_task"]["joint_pass"], 0)

    def test_prediction_gate_counts_reproduce_public_summaries(self):
        self.assertEqual(REPRO.prediction_gate_counts(), self.source["audit"]["original_validation"])
        self.assertEqual(sum(r["populations"] for r in REPRO.prediction_gate_counts().values()), 56)

    def test_prediction_summaries_reject_missing_or_unknown_population(self):
        path = REPRO.EVIDENCE / "prediction-audit.csv"
        with path.open(encoding="ascii", newline="") as stream:
            original = list(csv.DictReader(stream))
        variants = [original[:-1], copy.deepcopy(original), original + [original[0]]]
        variants[1][0]["from"] = "unmeasured"
        for rows in variants:
            stream = io.StringIO()
            writer = csv.DictWriter(stream, fieldnames=REPRO.PREDICTION_COLUMNS, lineterminator="\n")
            writer.writeheader()
            writer.writerows(rows)
            with patch.object(Path, "open", return_value=io.StringIO(stream.getvalue())), self.assertRaises(ValueError):
                REPRO.prediction_gate_counts()

    def test_no_private_artifact_bindings_in_source_record(self):
        forbidden = {"private_evidence_hashes", "public_reference_status", "artifact_sha256",
                     "frozen_transcription_sha256", "artifact_created_utc", "lock_created_utc",
                     "first_fit_receipt_utc", "last_fit_receipt_utc", "frozen_observation_utc"}
        def inspect(value):
            if isinstance(value, dict):
                self.assertFalse(set(value) & forbidden)
                for item in value.values():
                    inspect(item)
            elif isinstance(value, list):
                for item in value:
                    inspect(item)
        inspect(self.source)
        for record in self.source["public_data"]:
            self.assertRegex(record["sha256"], r"^[a-f0-9]{64}$")
        self.assertEqual({r["file"] for r in self.source["public_data"]},
                         {"fit-token-counts.csv", "prediction-audit.csv"})

    def test_public_inventory_and_privacy_scan(self):
        manifest = REPRO.read_json(REPRO.EVIDENCE / "manifest.json")
        actual = {str(p.relative_to(ROOT)) for p in REPRO.EVIDENCE.iterdir() if p.is_file()}
        actual |= {str(p.relative_to(ROOT)) for p in REPRO.DATA.iterdir() if p.is_file()}
        actual.add("scripts/reproduce-model-mix.py")
        self.assertEqual(actual - {"evidence/model-mix/manifest.json"}, set(manifest["sha256"]))
        patterns = ["/" + "Users/", "/" + "private/", "/" + "tmp/", "/" + "home/",
                    "sk" + r"-[A-Za-z0-9_-]{16,}", "Bearer" + r"\s+[A-Za-z0-9_-]{12,}",
                    "-----BEGIN " + r"(?:RSA |OPENSSH |EC )?PRIVATE KEY-----"]
        for name in actual:
            content = (ROOT / name).read_text(encoding="ascii")
            for pattern in patterns:
                self.assertIsNone(re.search(pattern, content), name + " violates public privacy scan")
        forbidden = {"prompt", "response", "answer", "raw_response", "api_key", "authorization",
                     "request_body", "answer_sha256", "raw_response_sha256", "account_id"}
        def inspect(value):
            if isinstance(value, dict):
                self.assertFalse(set(value) & forbidden)
                for item in value.values():
                    inspect(item)
            elif isinstance(value, list):
                for item in value:
                    inspect(item)
        for path in list(REPRO.EVIDENCE.glob("*.json")) + list(REPRO.DATA.glob("*.json")):
            inspect(REPRO.read_json(path))


if __name__ == "__main__":
    unittest.main()
