# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex

import json
from pathlib import Path
import tempfile
import unittest

from plot import load_series, render, reported_tokens


def report(before, after):
    return {
        "version": 1,
        "complete_observation_intervals": True,
        "before": {"start_unix_nano": 0, "duration_nano": 15,
                   "token_coverage": {"requests": 6, "missing_usage_requests": 2}},
        "after": {"start_unix_nano": 15, "duration_nano": 15,
                  "token_coverage": {"requests": 10, "missing_usage_requests": 2}},
        "counters": [
            {"name": "input_tokens", "unit": "reported-tokens", "before": before, "after": after},
            {"name": "output_tokens", "unit": "reported-tokens", "before": 0, "after": 0},
            {"name": "cache_read_input_tokens", "before": 99, "after": 99},
            {"name": "reasoning_output_tokens", "before": 99, "after": 99},
        ],
    }


class PlotTest(unittest.TestCase):
    def load(self, fleet, owned):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)
            for name, value in (("comparison.json", fleet), ("owned-only.json", owned)):
                (path / name).write_text(json.dumps(value), encoding="utf-8")
            return load_series(path)

    def test_demo_arithmetic_and_render(self):
        series = self.load(report(1200, 1800), report(800, 360))
        self.assertEqual(series["fleet"], [1200, 1800])
        self.assertEqual(series["ours"], [800, 360])
        self.assertEqual(series["partner"], [400, 1440])
        self.assertEqual(series["coverage"][1]["missing_usage_requests"], 2)
        image = render(series)
        self.assertEqual(image.size, (1200, 740))
        self.assertEqual(image.getpixel((410, 500)), (0, 119, 126))
        self.assertEqual(image.getpixel((888, 400)), (163, 59, 105))

    def test_missing_counters_are_not_zero(self):
        invalid = report(1200, 1800)
        invalid["counters"].pop(1)
        with self.assertRaisesRegex(ValueError, "input and output"):
            reported_tokens(invalid)

    def test_input_and_output_without_subsets(self):
        item = report(1000, 1500)
        item["counters"][1].update(before=200, after=300)
        self.assertEqual(reported_tokens(item), [1200, 1800])

    def test_unusable_reports_fail(self):
        for mutation in ("partial", "window", "duration", "negative", "zero", "unit"):
            with self.subTest(mutation=mutation):
                fleet, owned = report(1200, 1800), report(800, 360)
                if mutation == "partial":
                    fleet["complete_observation_intervals"] = False
                elif mutation == "window":
                    owned["after"]["start_unix_nano"] = 30
                elif mutation == "duration":
                    for item in (fleet, owned):
                        item["after"]["duration_nano"] = 30
                elif mutation == "negative":
                    owned["counters"][0]["after"] = 2000
                elif mutation == "zero":
                    owned["counters"][0]["before"] = 0
                elif mutation == "unit":
                    fleet["counters"][0]["unit"] = "dollars"
                with self.assertRaises(ValueError):
                    self.load(fleet, owned)


if __name__ == "__main__":
    unittest.main()
