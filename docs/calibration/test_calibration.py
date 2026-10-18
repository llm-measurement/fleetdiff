"""Offline scalar replay, exact-result, documentation and privacy checks."""

import copy
import csv
from decimal import Decimal
from fractions import Fraction
import hashlib
import io
import json
from pathlib import Path
import re
import socket
import subprocess
import sys
import unittest
from unittest.mock import patch
from urllib.parse import unquote, urlsplit

import calculate as calc
import reproduce as repro

PUBLIC_FILES = {
    "tasks.csv", "usage.csv", "exclusions.csv", "source-record.json", "calculate.py",
    "reproduce.py", "test_calibration.py", "expected-analysis.sha256", "expected-output.txt",
    "manifest.json", "README.md", "LICENSES.md",
}


class CalibrationTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tasks = repro.load_tasks()
        cls.rows = repro.load_usage(cls.tasks)
        cls.frozen = repro.load_frozen()
        cls.result = repro.analyze(cls.rows, cls.frozen)
        cls.source = repro.read_json(repro.HERE / "source-record.json")

    def invalid_csv(self, path, columns, rows, load):
        stream = io.StringIO()
        writer = csv.DictWriter(stream, fieldnames=columns, lineterminator="\n")
        writer.writeheader()
        writer.writerows(rows)
        with patch.object(Path, "open", return_value=io.StringIO(stream.getvalue())):
            with self.assertRaises(ValueError):
                load()

    def raw_usage(self):
        return repro.read_csv(repro.HERE / "usage.csv", repro.USAGE_COLUMNS)

    def test_complete_full_precision_output_and_page_table(self):
        self.assertEqual(repro.check(self.result), (repro.HERE / "expected-output.txt").read_text())
        digest = hashlib.sha256(repro.serialized(self.result).encode("ascii")).hexdigest()
        self.assertEqual(digest, (repro.HERE / "expected-analysis.sha256").read_text().strip())

    def test_sub_display_change_fails_exact_check(self):
        changed = copy.deepcopy(self.result)
        value = changed["strata"][0]["primary"]["observed_errors_usd"]
        value["median_signed_usd"] = str(Decimal(value["median_signed_usd"]) + Decimal("1e-26"))
        self.assertEqual(repro.table(changed), repro.table(self.result))
        with self.assertRaisesRegex(ValueError, "full-precision"):
            repro.check(changed)

    def test_cell_change_fails_even_when_table_unchanged(self):
        changed = copy.deepcopy(self.result)
        changed["cells"][0]["usage"]["input_tokens"] += 1
        self.assertEqual(repro.table(changed), repro.table(self.result))
        with self.assertRaisesRegex(ValueError, "full-precision"):
            repro.check(changed)

    def test_arithmetic_does_not_read_expected_results(self):
        original = Path.read_text
        def read(path, *args, **kwargs):
            self.assertFalse(path.name.startswith("expected"))
            self.assertNotEqual(path, repro.PAGE)
            return original(path, *args, **kwargs)
        with patch.object(Path, "read_text", read), patch.object(socket, "socket", side_effect=AssertionError("network")):
            self.assertEqual(repro.reproduce(), self.result)

    def test_input_change_changes_calculated_result(self):
        rows = copy.deepcopy(self.rows)
        rows[0]["input_tokens"] += 1
        rows[0]["ordinary_input_tokens"] += 1
        rows[0]["list_price_usd"] = calc.cold_cost(rows[0]["input_tokens"], rows[0]["output_tokens"],
                                                  self.frozen[2][rows[0]["model"]])
        changed = repro.analyze(rows, self.frozen)
        self.assertNotEqual(changed["strata"][0]["batches"][0], self.result["strata"][0]["batches"][0])
        with self.assertRaisesRegex(ValueError, "full-precision"):
            repro.check(changed)

    def test_cli_stdout_and_json(self):
        for option in ("--check", "--table", "--json"):
            run = subprocess.run([sys.executable, "-B", str(repro.HERE / "reproduce.py"), option],
                                 capture_output=True, text=True, check=True)
            self.assertEqual(run.stderr, "")
            expected = {"--check": repro.check(self.result), "--table": repro.table(self.result),
                        "--json": repro.serialized(self.result)}[option]
            self.assertEqual(run.stdout, expected)

    def test_unknown_fields_and_private_sentinel_rejected(self):
        rows = self.raw_usage()
        for row in rows:
            row["extra"] = "PRIVATE_SENTINEL_8f173c"
        self.invalid_csv(repro.HERE / "usage.csv", (*repro.USAGE_COLUMNS, "extra"), rows,
                         lambda: repro.load_usage(self.tasks))
        with patch.object(sys, "argv", ["reproduce.py", "--json"]), \
                patch.object(repro, "reproduce", side_effect=ValueError("PRIVATE_SENTINEL_8f173c")), \
                patch("sys.stdout", new_callable=io.StringIO) as out, \
                patch("sys.stderr", new_callable=io.StringIO) as err:
            self.assertEqual(repro.main(), 2)
            self.assertEqual(out.getvalue(), "")
            self.assertNotIn("PRIVATE_SENTINEL_8f173c", err.getvalue())

    def test_missing_duplicate_unknown_cells_rejected(self):
        variants = [self.raw_usage()[:-1]]
        for change in ({"sequence": "0"}, {"task_id": "not-a-public-id"}, {"model": "other"},
                       {"status": "retried"}, {"input_tokens": "-1"}, {"input_tokens": "True"},
                       {"output_tokens": "1.5"}, {"nonempty": "true"}, {"list_price_usd": "NaN"},
                       {"list_price_usd": "1e-3"}, {"stop_reason": "PRIVATE_SENTINEL_8f173c"}):
            rows = self.raw_usage()
            rows[0].update(change)
            variants.append(rows)
        rows = self.raw_usage()
        rows[1].update(task_id=rows[0]["task_id"], model=rows[0]["model"])
        variants.append(rows)
        for rows in variants:
            self.invalid_csv(repro.HERE / "usage.csv", repro.USAGE_COLUMNS, rows,
                             lambda: repro.load_usage(self.tasks))

    def test_null_and_zero_are_distinct(self):
        rows = self.raw_usage()
        missing = next(r for r in rows if r["status"] == "missing")
        missing["input_tokens"] = "0"
        self.invalid_csv(repro.HERE / "usage.csv", repro.USAGE_COLUMNS, rows,
                         lambda: repro.load_usage(self.tasks))
        accepted = next(r for r in self.rows if r["status"] == "ok")
        self.assertEqual(accepted["cache_read_tokens"], 0)
        unknown = next(r for r in self.rows if r["status"] == "missing")
        self.assertIsNone(unknown["cache_read_tokens"])

    def test_exact_cost_and_token_partition_checks(self):
        for change in ({"list_price_usd": Decimal("0.00299000000000000000000001")},
                       {"ordinary_input_tokens": 0}, {"cache_read_tokens": 1},
                       {"output_tokens": 4097}, {"reasoning_tokens": 4097},
                       {"response_valid": False}):
            row = dict(self.rows[0], **change)
            with self.subTest(change=change), self.assertRaises(ValueError):
                repro.inspect_cell(row, self.frozen[2][row["model"]])

    def test_frozen_inputs_are_byte_pinned(self):
        with patch.object(Path, "read_bytes", return_value=b"{}"), self.assertRaises(ValueError):
            repro.load_frozen()
        self.assertEqual(self.source["frozen_sha256"], repro.PINS)

    def test_json_rejects_duplicates_and_nonfinite_values(self):
        for text in ('{"x": 1, "x": 2}', '{"x":NaN}', '{"x":Infinity}'):
            with patch.object(Path, "read_text", return_value=text), self.assertRaises(ValueError):
                repro.read_json(Path("unused"))

    def test_task_allocation_and_exclusion_proof(self):
        excluded = repro.check_exclusions(self.tasks)
        self.assertEqual(len(self.tasks), calc.BATCHES * sum(calc.BATCH_SIZES.values()))
        self.assertEqual(len({r["context_sha256"] for r in self.tasks.values()}), len(self.tasks))
        for row in excluded:
            self.assertNotIn(row["task_id"], self.tasks)
        changed = copy.deepcopy(self.tasks)
        next(iter(changed.values()))["context_sha256"] = excluded[0]["context_sha256"]
        with self.assertRaisesRegex(ValueError, "prior context"):
            repro.check_exclusions(changed)
        changed = dict(self.tasks)
        changed[excluded[0]["task_id"]] = next(iter(self.tasks.values()))
        with self.assertRaisesRegex(ValueError, "prior task"):
            repro.check_exclusions(changed)

    def test_duplicate_task_group_and_rank_rejected(self):
        original = repro.read_csv(repro.HERE / "tasks.csv", repro.TASK_COLUMNS)
        variants = [original[:-1]]
        for key in ("task_id", "context_sha256", "within_batch_1based"):
            rows = copy.deepcopy(original)
            rows[1][key] = rows[0][key]
            variants.append(rows)
        rows = copy.deepcopy(original)
        rows[0]["within_batch_1based"], rows[1]["within_batch_1based"] = (
            rows[1]["within_batch_1based"], rows[0]["within_batch_1based"])
        variants.append(rows)
        for rows in variants:
            self.invalid_csv(repro.HERE / "tasks.csv", repro.TASK_COLUMNS, rows, repro.load_tasks)

    def test_registered_design_and_price_observation(self):
        design = self.source["design"]
        self.assertEqual(design["planned_batches"], calc.BATCHES)
        self.assertEqual(design["batch_sizes"], calc.BATCH_SIZES)
        self.assertEqual(tuple(design["models"]), calc.MODELS)
        self.assertEqual(Decimal(design["movement_fraction"]), calc.MOVE)
        self.assertEqual(Decimal(design["wilson_z"]), calc.Z95)
        self.assertEqual(design["decimal_precision"], calc.PRECISION)
        self.assertEqual(design["selection_seed"], repro.SEED)
        self.assertEqual(self.source["price_check"]["date"], self.source["execution_date"])
        prices = repro.read_json(repro.ROOT / "internal/reprice/data/prices.json")
        self.assertEqual(self.source["price_check"]["models"],
                         {r["id"]: {k: r[k] for k in ("input", "output", "cache_read", "cache_write")}
                          for r in prices["models"]})
        self.assertLess(self.source["registration_date"], self.source["execution_date"])

    def test_stop_is_preserved_without_retry_or_replacement(self):
        stop = self.source["stop"]
        stopped = [r for r in self.rows if r["status"] == "stop"]
        self.assertEqual(len(stopped), 1)
        self.assertEqual({k: stopped[0][k] for k in ("sequence", "task_id", "model")},
                         {k: stop[k] for k in ("sequence", "task_id", "model")})
        self.assertEqual(stopped[0]["stop_reason"], stop["reason"])
        self.assertTrue(all(r["status"] == "ok" for r in self.rows[:stop["sequence"] - 1]))
        self.assertTrue(all(r["status"] == "missing" for r in self.rows[stop["sequence"]:]))
        self.assertEqual(self.source["design"]["retries"], 0)
        self.assertEqual(self.source["design"]["replacements"], 0)

    def test_all_twelve_cases_and_coding_missingness(self):
        self.assertEqual(len(self.result["strata"]), len(calc.DIRECTIONS) * len(calc.BATCH_SIZES))
        for stratum in self.result["strata"]:
            primary = stratum["primary"]
            self.assertEqual(primary["n"], calc.BATCHES)
            self.assertEqual(primary["wilson95"]["n"], calc.BATCHES)
            if stratum["workload"] == "coding":
                self.assertEqual((primary["available_n"], primary["unavailable_n"]), (10, 10))
                self.assertEqual(primary["p90_relative_width"], "Infinity")
                self.assertEqual(primary["observed_errors_usd"]["n"], 10)
                self.assertFalse(primary["gates"]["pass"])
                for batch in stratum["batches"][10:]:
                    self.assertFalse(batch["primary"]["covered"])
                    self.assertIsNone(batch["primary"]["signed_error_usd"])
            else:
                self.assertEqual((primary["available_n"], primary["unavailable_n"]), (20, 0))
        passes = [(s["from"], s["to"], s["workload"], s["primary"]["covered"])
                  for s in self.result["strata"] if s["primary"]["gates"]["pass"]]
        self.assertEqual(passes, [(calc.MODELS[1], calc.MODELS[3], "qa", 19),
                                  (calc.MODELS[1], calc.MODELS[3], "extraction", 17)])
        other = [s["primary"]["covered"] for s in self.result["strata"]
                 if s["workload"] != "coding" and not s["primary"]["gates"]["pass"]]
        self.assertEqual(len(other), 6)
        self.assertTrue(all(2 <= covered <= 14 for covered in other))
        self.assertFalse(any(s["secondary_pooled_envelope"]["gates"]["pass"] for s in self.result["strata"]))

    def test_direction_local_missingness(self):
        source = [r for r in self.result["cells"] if r["workload"] == "coding" and
                  r["batch_1based"] == 11 and r["model"] == calc.MODELS[0]]
        target = [r for r in self.result["cells"] if r["workload"] == "coding" and
                  r["batch_1based"] == 11 and r["model"] == calc.MODELS[1]]
        batch = calc.batch_result(11, source, target, calc.DIRECTIONS[0], "coding", self.frozen)
        self.assertFalse(batch["primary"]["available"])
        self.assertIsNone(batch["source_token_totals"])

    def test_width_gates_use_exact_moved_cost_not_full_scenario(self):
        for stratum in self.result["strata"]:
            for batch in stratum["batches"]:
                observation = batch["primary"]
                if observation["available"]:
                    lo, hi, actual = (Decimal(observation[k]) for k in ("lower_usd", "upper_usd", "actual_usd"))
                    self.assertEqual(calc.json_safe(calc.relative_width(lo, hi, actual)), observation["relative_width"])
                    self.assertEqual(observation["covered"], lo <= actual <= hi)
                    self.assertEqual(Decimal(batch["full_scenario"]["actual_usd"]),
                                     Decimal(batch["full_scenario"]["unmoved_source_usd"]) + actual)

    def test_gate_boundaries_width_ranks_and_observed_error_median(self):
        observations = [calc.observation(Decimal(1), Decimal("1.5"), Decimal(1), Decimal("1.2"))
                        for _ in range(16)] + [calc.observation() for _ in range(4)]
        result = calc.summarize(observations)
        self.assertTrue(result["gates"]["coverage"])
        self.assertTrue(result["gates"]["median_width"])
        self.assertFalse(result["gates"]["p90_width"])
        self.assertEqual(result["median_relative_width"], Fraction(1, 2))
        self.assertEqual(result["p90_relative_width"], calc.INF)
        self.assertEqual(result["observed_errors_usd"]["median_signed_usd"], Decimal("0.2"))
        self.assertEqual(result["observed_errors_usd"]["n"], 16)
        observations[15] = calc.observation()
        self.assertFalse(calc.summarize(observations)["gates"]["coverage"])

    def test_zero_cost_and_wilson_boundaries(self):
        self.assertEqual(calc.relative_width(Decimal(0), Decimal(0), Decimal(0)), Fraction(0))
        self.assertEqual(calc.relative_width(Decimal(0), Decimal(1), Decimal(0)), calc.INF)
        self.assertEqual(calc.wilson95(0)["lower"], 0)
        self.assertEqual(calc.wilson95(20)["upper"], 1)
        self.assertEqual(calc.wilson95(10)["lower"] + calc.wilson95(10)["upper"], 1)

    def test_publication_manifest_and_inventory(self):
        self.assertEqual({p.name for p in repro.HERE.iterdir() if p.is_file()}, PUBLIC_FILES)
        manifest = repro.read_json(repro.HERE / "manifest.json")
        names = {"docs/calibration/" + name for name in PUBLIC_FILES - {"manifest.json"}}
        names |= {"docs/WHAT_IF_CALIBRATION.md", "docs/WHAT_IF.md", ".github/workflows/ci.yml",
                  "internal/reprice/data/factors.json", "internal/reprice/data/prices.json"}
        self.assertEqual(set(manifest), {"schema", "sha256"})
        self.assertEqual(set(manifest["sha256"]), names)
        for name, expected in manifest["sha256"].items():
            self.assertEqual(hashlib.sha256((repro.ROOT / name).read_bytes()).hexdigest(), expected, name)

    def test_privacy_inventory_and_structured_allowlist(self):
        patterns = ["/" + "Users/", "/" + "private/", "/" + "tmp/", "/" + "home/",
                    "Fleet" + "bound", "docs/" + "local", "reviewer" + "_name",
                    "sk" + r"-[A-Za-z0-9_-]{16,}", "Bearer" + r"\s+[A-Za-z0-9_-]{12,}",
                    "-----BEGIN " + r"(?:RSA |OPENSSH |EC )?PRIVATE KEY-----"]
        for path in [*(repro.HERE / name for name in PUBLIC_FILES), repro.PAGE]:
            text = path.read_text(encoding="ascii")
            for pattern in patterns:
                self.assertIsNone(re.search(pattern, text, re.I), path.name)
        forbidden = {"prompt", "answer", "response", "api_key", "authorization", "request_body",
                     "request_sha256", "receipt_sha256", "answer_sha256", "raw_response_sha256",
                     "manifest_sha256", "analysis_module_sha256", "amendment", "account_id"}
        def inspect(value):
            if isinstance(value, dict):
                self.assertFalse(set(value) & forbidden)
                for item in value.values():
                    inspect(item)
            elif isinstance(value, list):
                for item in value:
                    inspect(item)
        inspect(self.source)
        inspect(self.result)

    def test_document_links_and_requested_wording(self):
        for path in (repro.PAGE, repro.ROOT / "docs/WHAT_IF.md", repro.HERE / "README.md", repro.HERE / "LICENSES.md"):
            for link in re.findall(r"\]\(([^)\s]+)\)", path.read_text()):
                parsed = urlsplit(link)
                if parsed.scheme:
                    self.assertEqual(parsed.scheme, "https")
                    continue
                target = (path.parent / unquote(parsed.path)).resolve()
                self.assertTrue(target.exists(), link)
                if parsed.fragment:
                    headings = re.findall(r"^#{1,6} (.+)$", target.read_text(), re.M)
                    anchors = {re.sub(r"[^\w\s-]", "", h.lower()).replace(" ", "-") for h in headings}
                    anchors |= set(re.findall(r'<a id="([^"]+)"', target.read_text()))
                    self.assertIn(parsed.fragment, anchors)
        text = " ".join((repro.ROOT / "docs/WHAT_IF.md").read_text().split())
        expected = ("The factors are geometric-mean paired ratios; their endpoints are approximate 95% "
                    "intervals for the log mean. They describe the uncertainty of the average factor, "
                    "not a range for your traffic. In a fresh calibration, they reliably contained "
                    "measured 30-task batch costs only for Sol to Luna QA and extraction; "
                    "[see the calibration](WHAT_IF_CALIBRATION.md). Endpoint sums and the workload "
                    "envelope are sensitivity calculations.")
        self.assertIn(expected, text)
        self.assertEqual(text.count("WHAT_IF_CALIBRATION.md"), 1)
        self.assertEqual(repro.PAGE.read_text().count("## What The Numbers Mean"), 1)


if __name__ == "__main__":
    unittest.main()
