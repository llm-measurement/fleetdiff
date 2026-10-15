#!/usr/bin/env python3
"""Reproduce the public model-mix fit with Python 3.11+ stdlib, offline."""

import argparse
import csv
from decimal import Decimal, ROUND_HALF_EVEN, localcontext
import hashlib
import json
import math
from pathlib import Path
import re
import statistics

ROOT = Path(__file__).resolve().parents[1]
EVIDENCE = ROOT / "evidence/model-mix"
DATA = ROOT / "internal/reprice/data"
DATE = "2026-10-14"
MODELS = ("claude-sonnet-5-5", "gpt-6.1-sol", "claude-haiku-5-5", "gpt-6-luna")
PROVIDERS = dict(zip(MODELS, ("anthropic", "openai", "anthropic", "openai")))
DIRECTIONS = ((MODELS[0], MODELS[1]), (MODELS[0], MODELS[2]),
              (MODELS[0], MODELS[3]), (MODELS[1], MODELS[3]))
WORKLOADS = ("qa", "extraction", "coding")
COLUMNS = ("workload", "split_index", "task_id", "model", "input_tokens", "output_tokens")
PREDICTION_COLUMNS = ("method", "population", "from", "to", "workload", "n", "covered",
                      "coverage_required", "median_normalized_width", "p90_normalized_width")
SOURCE = "https://raw.githubusercontent.com/llm-measurement/fleetdiff/main/evidence/model-mix/source-record.json"
METHOD = "geometric_mean_paired_ratios_normal_approximate_log_mean_95"
DECIMAL = re.compile(r"(?:0|[1-9][0-9]*)(?:\.[0-9]+)?\Z")
Z95 = 1.959963984540054


def require(condition, message):
    if not condition:
        raise ValueError(message)


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, "duplicate JSON member")
        result[key] = value
    return result


def read_json(path):
    return json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=unique_object,
                      parse_constant=lambda _: require(False, "nonfinite JSON"))


def decimal_string(value):
    require(isinstance(value, str) and DECIMAL.fullmatch(value), "expected decimal string")
    return Decimal(value)


def load_counts(path=EVIDENCE / "fit-token-counts.csv"):
    with path.open(encoding="ascii", newline="") as stream:
        reader = csv.DictReader(stream)
        require(reader.fieldnames == list(COLUMNS), "numeric export allowlist mismatch")
        rows = list(reader)
    require(len(rows) == 468, "expected 468 fit-only observations")
    seen, tasks, groups = set(), {}, {}
    for row in rows:
        require(set(row) == set(COLUMNS) and all(isinstance(v, str) for v in row.values()),
                "unexpected CSV fields")
        require(row["workload"] in WORKLOADS and row["model"] in MODELS, "unknown identity")
        for key in ("split_index", "input_tokens", "output_tokens"):
            require(re.fullmatch(r"0|[1-9][0-9]*", row[key]), "invalid integer")
            row[key] = int(row[key])
        require(0 <= row["split_index"] < 39, "non-fitting task")
        require(all(0 < row[k] <= 4096 for k in ("input_tokens", "output_tokens")),
                "zero or out-of-policy count; no smoothing")
        pattern = r"HumanEval-[0-9]{1,3}" if row["workload"] == "coding" else r"dolly-[0-9]{5}"
        require(re.fullmatch(pattern, row["task_id"]), "non-allowlisted task identifier")
        identity = row["workload"], row["split_index"], row["model"]
        require(identity not in seen, "duplicate observation")
        seen.add(identity)
        task = row["workload"], row["split_index"]
        require(tasks.setdefault(task, row["task_id"]) == row["task_id"], "unpaired task")
        require(groups.setdefault(row["task_id"], task) == task, "task reused across split or workload")
    expected = {(w, i, m) for w in WORKLOADS for i in range(39) for m in MODELS}
    require(seen == expected and len(groups) == 117, "incomplete paired fit population")
    return rows


def estimate(pairs):
    """Keep the frozen estimator, including its float sample-SD approximation."""
    require(len(pairs) in (39, 117), "unsupported fit size")
    require(all(type(a) is int and type(b) is int and a > 0 and b > 0 for a, b in pairs),
            "undefined ratio; no omission or smoothing")
    with localcontext() as context:
        context.prec = 50
        logs = [(Decimal(b) / Decimal(a)).ln() for a, b in pairs]
        mean = sum(logs) / Decimal(len(logs))
        sd = statistics.stdev(float(value) for value in logs)
        margin = Decimal(str(Z95 * sd / math.sqrt(len(logs))))
        return {"factor": format(mean.exp(), "f"),
                "low": format((mean - margin).exp(), "f"),
                "high": format((mean + margin).exp(), "f"), "n": len(logs)}


def shipped_decimal(value):
    with localcontext() as context:
        context.prec = 50
        context.rounding = ROUND_HALF_EVEN
        return format(Decimal(value).quantize(Decimal("0.000000000000000001")), "f").rstrip("0").rstrip(".")


def reproduce(rows):
    lookup = {(r["workload"], r["split_index"], r["model"]): r for r in rows}
    estimates, inputs, outputs = [], [], []
    for source, target in DIRECTIONS:
        pooled = []
        for workload in WORKLOADS:
            def ratios(field):
                return [(lookup[workload, i, source][field], lookup[workload, i, target][field])
                        for i in range(39)]
            input_counts = ratios("input_tokens")
            pooled.extend(input_counts)
            input_fit, output_fit = estimate(input_counts), estimate(ratios("output_tokens"))
            estimates.append({"from": source, "to": target, "workload": workload,
                              "input": input_fit, "output": output_fit})
            outputs.append({"from": source, "to": target, "workload": workload,
                            "output": shipped_decimal(output_fit["factor"]),
                            "output_low": shipped_decimal(output_fit["low"]),
                            "output_high": shipped_decimal(output_fit["high"]), "n": 39,
                            "source_record": SOURCE + "#original-fit"})
        pooled_fit = estimate(pooled)
        if PROVIDERS[source] == PROVIDERS[target]:
            require(all(a == b for a, b in pooled), "within-provider identity contradicted in fit")
        inputs.append({"from": source, "to": target, "factor": shipped_decimal(pooled_fit["factor"]),
                       "n": 117, "source_record": SOURCE + "#descriptive-input-pool"})
    factors = {"version": 1, "date": DATE, "source_record": SOURCE, "method": METHOD,
               "input_pairs": inputs, "pairs": outputs}
    diagnostics = {"version": 1, "date": DATE, "method": METHOD,
                   "interval": "approximate_95_percent_population_geometric_mean_not_prediction",
                   "strata": estimates, "input_pairs": []}
    for source, target in DIRECTIONS:
        pairs = [(lookup[w, i, source]["input_tokens"], lookup[w, i, target]["input_tokens"])
                 for w in WORKLOADS for i in range(39)]
        diagnostics["input_pairs"].append({"from": source, "to": target, **estimate(pairs)})
    return factors, diagnostics


def prediction_gate_counts(path=EVIDENCE / "prediction-audit.csv"):
    with path.open(encoding="ascii", newline="") as stream:
        reader = csv.DictReader(stream)
        require(reader.fieldnames == list(PREDICTION_COLUMNS), "prediction summary allowlist mismatch")
        rows = list(reader)
    expected = {(m, p, a, b, w) for m in ("workload", "pooled") for a, b in DIRECTIONS
                for p in ("per_task", "six_task_batches", "mixed_18_batches")
                for w in (("mixed",) if p == "mixed_18_batches" else WORKLOADS)}
    seen, result = set(), {}
    for row in rows:
        require(set(row) == set(PREDICTION_COLUMNS) and
                all(isinstance(v, str) for v in row.values()), "unexpected prediction summary fields")
        identity = tuple(row[k] for k in ("method", "population", "from", "to", "workload"))
        require(identity in expected and identity not in seen, "unknown or duplicate prediction population")
        seen.add(identity)
        for key in ("n", "covered", "coverage_required"):
            require(re.fullmatch(r"0|[1-9][0-9]*", row[key]), "invalid prediction count")
            row[key] = int(row[key])
        required_n, threshold = (78, 63) if row["population"] == "per_task" else (13, 11)
        require(row["n"] == required_n and row["coverage_required"] == threshold and
                0 <= row["covered"] <= row["n"], "invalid prediction population size or gate")
        median = decimal_string(row["median_normalized_width"])
        p90 = decimal_string(row["p90_normalized_width"])
        require(0 <= median <= p90, "invalid audited widths")
        coverage, width = row["covered"] >= threshold, median <= Decimal("0.50") and p90 <= Decimal("1.00")
        counts = result.setdefault(row["method"] + "|" + row["population"],
                                   {"populations": 0, "coverage_pass": 0, "width_pass": 0, "joint_pass": 0})
        for key, value in (("populations", 1), ("coverage_pass", coverage),
                           ("width_pass", width), ("joint_pass", coverage and width)):
            counts[key] += int(value)
    require(seen == expected, "missing prediction population")
    return result


def check_public():
    rows = load_counts()
    factors, diagnostics = reproduce(rows)
    require(read_json(DATA / "factors.json") == factors, "shipped factors differ from measured fit")
    require(read_json(EVIDENCE / "fit-estimates.json") == diagnostics, "fit intervals differ")
    source = read_json(EVIDENCE / "source-record.json")
    require(source["version"] == 1 and source["date"] == DATE, "source version/date mismatch")
    require(source["original-fit"]["estimates"] == diagnostics["strata"], "frozen fit mismatch")
    require(prediction_gate_counts() == source["audit"]["original_validation"], "prediction gate totals differ")
    require(source["audit"]["width_gates"] == {
        "median_normalized_width_max": "0.50", "p90_normalized_width_max": "1.00"}, "prediction width gates differ")
    require({r["file"] for r in source["public_data"]} == {"fit-token-counts.csv", "prediction-audit.csv"}
            and len(source["public_data"]) == 2, "public data binding mismatch")
    for record in source["public_data"]:
        raw = (EVIDENCE / record["file"]).read_bytes()
        require(hashlib.sha256(raw).hexdigest() == record["sha256"], "public data SHA-256 mismatch")
        require(len(raw.splitlines()) - 1 == record["rows"], "public data row count mismatch")
    prices = read_json(DATA / "prices.json")
    require(prices["version"] == 1 and prices["date"] == DATE and
            prices["source_record"] == SOURCE + "#prices", "price metadata mismatch")
    require(prices["currency"] == "USD" and prices["unit"] == "per_million_tokens" and
            prices["tier"] == source["prices"]["tier"], "price units/tier mismatch")
    require(len(prices["models"]) == 4 and {m["id"] for m in prices["models"]} == set(MODELS),
            "price catalog identities differ")
    for model in prices["models"]:
        require(model["provider"] == PROVIDERS[model["id"]], "price provider mismatch")
        require(model.get("input_tier_max") == source["prices"]["short_input_limit"].get(model["id"]),
                "price tier bound mismatch")
        require(model["cache_write_ttl"] == source["prices"]["cache_write_scope"][model["provider"]],
                "cache write TTL mismatch")
        for field in ("input", "output", "cache_read", "cache_write"):
            require(decimal_string(model[field]) > 0, "nonpositive price")
            require(model[field] == source["prices"]["models"][model["id"]][field],
                    "price differs from dated transcription")
    for name, digest in read_json(EVIDENCE / "manifest.json")["sha256"].items():
        path = ROOT / name
        require(path.is_file() and not path.is_symlink() and ROOT in path.resolve().parents,
                "unsafe manifest path")
        require(hashlib.sha256(path.read_bytes()).hexdigest() == digest, "public hash mismatch")
    print("PASS: 468 numeric fit observations; 12 output strata; 4 directed input pools; frozen prices")
    print("PASS: 56 public prediction summaries reproduce the original gate-count totals.")
    print("Intervals describe fitted geometric population means, not predictions or bill coverage.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true", help="check bundled artifacts (the default)")
    parser.add_argument("--json", action="store_true", help="print recomputed factors without writing")
    args = parser.parse_args()
    if args.json:
        print(json.dumps(reproduce(load_counts())[0], indent=2, allow_nan=False))
    else:
        check_public()


if __name__ == "__main__":
    main()
