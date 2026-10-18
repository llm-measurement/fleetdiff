#!/usr/bin/env python3
"""Reproduce the fresh-batch calibration from public scalar records, offline."""

import argparse
from collections import Counter, defaultdict
import csv
from decimal import Decimal
import hashlib
import json
from pathlib import Path
import re
import sys

import calculate as calc

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
PINS = {
    "factors": "cd1b087e294a3d9768cc52b8dd8a6d0b2ab169bcb8b979bc44957310869d2b99",
    "prices": "5d0e1af2d161af4823e92f00123b3b724b5b7f528d5984cfcc05242e189d252f",
    "calculation": "6550dda842db930bcad592518b19f8a923564e7ed8f531af916312cccb547372",
}
TASK_COLUMNS = ("workload", "batch_1based", "within_batch_1based", "task_id", "context_sha256")
USAGE_COLUMNS = ("sequence", "task_id", "model", "status", "stop_reason",
                 *calc.TOKEN_FIELDS, *calc.FLAGS, "list_price_usd", *calc.LIABILITY_FIELDS)
EXCLUSION_COLUMNS = ("kind", "task_id", "context_sha256")
TABLE_START = "<!-- calibration-table:start -->"
TABLE_END = "<!-- calibration-table:end -->"
PAGE = ROOT / "docs/WHAT_IF_CALIBRATION.md"
SHA256 = re.compile(r"[0-9a-f]{64}\Z")
SEED = "fleetdiff-aggregate-v1-20261015"
require = calc.require


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, "duplicate JSON member")
        result[key] = value
    return result


def read_json(path):
    return json.loads(path.read_text(encoding="ascii"), object_pairs_hook=unique_object,
                      parse_float=Decimal,
                      parse_constant=lambda _: require(False, "nonfinite JSON"))


def read_csv(path, columns):
    with path.open(encoding="ascii", newline="") as stream:
        reader = csv.DictReader(stream)
        require(tuple(reader.fieldnames or ()) == columns, "unexpected CSV columns")
        rows = list(reader)
    require(all(set(row) == set(columns) and all(v is not None for v in row.values())
                for row in rows), "malformed CSV record")
    return rows


def integer(value, maximum=2**64 - 1):
    require(isinstance(value, str) and re.fullmatch(r"0|[1-9][0-9]*", value) is not None,
            "invalid integer")
    require(len(value) <= 20 and int(value) <= maximum, "integer exceeds bound")
    return int(value)


def money(value):
    require(isinstance(value, str) and len(value) <= 100 and
            re.fullmatch(r"[0-9]+(?:\.[0-9]+)?", value) is not None, "invalid decimal price")
    result = Decimal(value)
    require(result <= 1000000 and len(result.as_tuple().digits) <= 40 and
            result.as_tuple().exponent >= -40, "unsupported price precision")
    return result


def load_frozen():
    values = {}
    for name in ("factors", "prices"):
        path = ROOT / "internal/reprice/data" / (name + ".json")
        require(hashlib.sha256(path.read_bytes()).hexdigest() == PINS[name], "frozen input changed")
        values[name] = read_json(path)
    inputs = {(r["from"], r["to"]): Decimal(r["factor"])
              for r in values["factors"]["input_pairs"]}
    outputs = {(r["from"], r["to"], r["workload"]): {
        name: Decimal(r[field]) for name, field in
        (("point", "output"), ("lower", "output_low"), ("upper", "output_high"))
    } for r in values["factors"]["pairs"]}
    rates = {r["id"]: {k: Decimal(r[k]) for k in ("input", "output")}
             for r in values["prices"]["models"]}
    return inputs, outputs, rates


def load_tasks():
    rows = read_csv(HERE / "tasks.csv", TASK_COLUMNS)
    tasks, groups, positions = {}, set(), set()
    for row in rows:
        workload, task, group = row["workload"], row["task_id"], row["context_sha256"]
        require(workload in calc.BATCH_SIZES, "unknown workload")
        pattern = r"mbpp-[1-9][0-9]{0,2}" if workload == "coding" else r"dolly-[0-9]{5}"
        require(re.fullmatch(pattern, task) is not None, "invalid public task ID")
        row["batch_1based"] = integer(row["batch_1based"], calc.BATCHES)
        row["within_batch_1based"] = integer(row["within_batch_1based"], calc.BATCH_SIZES[workload])
        require(row["batch_1based"] > 0 and row["within_batch_1based"] > 0, "invalid task position")
        require(SHA256.fullmatch(group) is not None, "invalid context group")
        position = (workload, row["batch_1based"], row["within_batch_1based"])
        require(task not in tasks and group not in groups and position not in positions,
                "reused task, group or position")
        tasks[task] = row
        groups.add(group)
        positions.add(position)
    require(len(tasks) == calc.BATCHES * sum(calc.BATCH_SIZES.values()), "incomplete task allocation")
    for workload in calc.BATCH_SIZES:
        selected = [r for r in rows if r["workload"] == workload]
        ranked = sorted(selected, key=lambda r: (
            hashlib.sha256(f"{SEED}|{workload}|{r['context_sha256']}".encode("ascii")).hexdigest(),
            r["task_id"]))
        ordered = sorted(selected, key=lambda r: (r["batch_1based"], r["within_batch_1based"]))
        require(ranked == ordered, "selected task ranking changed")
    return tasks


def check_exclusions(tasks):
    rows = read_csv(HERE / "exclusions.csv", EXCLUSION_COLUMNS)
    seen = set()
    for row in rows:
        require(row["kind"] in {"fit", "calibration", "evaluation", "pilot", "cache"},
                "unknown exclusion kind")
        require(SHA256.fullmatch(row["context_sha256"]) is not None, "invalid exclusion group")
        if row["kind"] in {"pilot", "cache"}:
            require(row["task_id"] == "", "synthetic exclusions carry groups only")
        else:
            require(re.fullmatch(r"dolly-[0-9]{5}|HumanEval/[0-9]{1,3}", row["task_id"]) is not None,
                    "invalid prior public task ID")
        key = tuple(row[k] for k in EXCLUSION_COLUMNS)
        require(key not in seen, "duplicate exclusion")
        seen.add(key)
    require(Counter(r["kind"] for r in rows) ==
            {"fit": 117, "calibration": 117, "evaluation": 234, "pilot": 2, "cache": 13},
            "incomplete historical exclusions")
    require(not set(tasks) & {r["task_id"] for r in rows}, "prior task reused")
    require(not {r["context_sha256"] for r in tasks.values()} &
            {r["context_sha256"] for r in rows}, "prior context group reused")
    return rows


def load_usage(tasks):
    rows = read_csv(HERE / "usage.csv", USAGE_COLUMNS)
    require(len(rows) == len(tasks) * len(calc.MODELS), "incomplete planned cells")
    identities = set()
    for sequence, row in enumerate(rows, 1):
        row["sequence"] = integer(row["sequence"])
        require(row["sequence"] == sequence, "nonconsecutive sequence")
        task, model = row["task_id"], row["model"]
        require(task in tasks and model in calc.MODELS, "unknown task/model")
        require((task, model) not in identities, "duplicate task/model")
        identities.add((task, model))
        require(row["status"] in {"ok", "stop", "missing"}, "unknown cell status")
        require(row["stop_reason"] == ("receipt_finality" if row["status"] == "stop" else ""),
                "unexpected stop reason")
        for key in calc.TOKEN_FIELDS + calc.LIABILITY_FIELDS:
            row[key] = integer(row[key]) if row[key] else None
        for key in calc.FLAGS:
            require(row[key] in {"", "0", "1"}, "invalid scalar flag")
            row[key] = {"": None, "0": False, "1": True}[row[key]]
        row["list_price_usd"] = money(row["list_price_usd"]) if row["list_price_usd"] else None
        if row["status"] != "ok":
            require(all(row[k] is None for k in calc.TOKEN_FIELDS + calc.FLAGS +
                        ("list_price_usd", "settled_with_contingency_micro_usd")),
                    "unavailable cell has accepted usage")
            if row["status"] == "missing":
                require(row["reserved_micro_usd"] is None, "missing cell has a reservation")
        else:
            required = (*calc.TOKEN_FIELDS[:-1], *calc.FLAGS, "list_price_usd", *calc.LIABILITY_FIELDS)
            require(all(row[k] is not None for k in required), "incomplete accepted record")
            require(row["reasoning_tokens"] is not None or model.startswith("claude-"),
                    "missing reasoning count")
        row.update(workload=tasks[task]["workload"], batch_1based=tasks[task]["batch_1based"])
    return rows


def inspect_cell(row, rates):
    result = {k: row[k] for k in ("sequence", "model", "workload", "batch_1based")}
    missing = row["status"] == "missing"
    result.update(available=False, reasons=[], status="unavailable" if missing else row["status"],
                  flags={k: row[k] for k in calc.FLAGS}, usage={k: row[k] for k in calc.TOKEN_FIELDS},
                  liability={k: row[k] for k in calc.LIABILITY_FIELDS},
                  reported_list_price_usd=row["list_price_usd"], cold_cost_usd=None,
                  price_reconciled=None, identity_verified=not missing)
    reasons = result["reasons"]
    if missing:
        reasons.append("missing_receipt")
        return result
    if row["status"] != "ok":
        reasons.append("status_not_ok")
    for key in calc.FLAGS:
        if row[key] is None:
            reasons.append("missing_or_invalid_" + key)
    if row["nonempty"] is False:
        reasons.append("empty_response")
    if row["truncated"] is True:
        reasons.append("truncated")
    if row["response_valid"] is False:
        reasons.append("response_invalid")
    if all(row[k] is not None for k in ("nonempty", "truncated", "response_valid")):
        require(row["response_valid"] == (row["nonempty"] and not row["truncated"]),
                "inconsistent response flags")
    for key in calc.TOKEN_FIELDS:
        if row[key] is None and not (key == "reasoning_tokens" and
                                    row["model"].startswith("claude-") and row["status"] == "ok"):
            reasons.append("missing_or_invalid_" + key)
    if row["list_price_usd"] is None:
        reasons.append("list_price_unavailable")
    if row["status"] == "ok":
        require(row["input_tokens"] == sum(row[k] for k in calc.TOKEN_FIELDS[1:4]),
                "invalid input partition")
        require(row["cache_read_tokens"] == row["cache_write_tokens"] == 0, "unexpected cache")
        require(row["input_tokens"] <= 4096 and row["output_tokens"] <= 4096, "usage over bound")
        require(row["reasoning_tokens"] is None or row["reasoning_tokens"] <= row["output_tokens"],
                "invalid reasoning partition")
        cost = calc.cold_cost(row["input_tokens"], row["output_tokens"], rates)
        require(cost == row["list_price_usd"], "exact receipt price mismatch")
        result.update(cold_cost_usd=cost, price_reconciled=True)
    result["available"] = not reasons
    return result


def analyze(rows, frozen):
    cells, groups = [], defaultdict(list)
    for row in rows:
        cell = inspect_cell(row, frozen[2][row["model"]])
        cells.append(cell)
        groups[row["workload"], row["batch_1based"], row["model"]].append(cell)
    strata = []
    for source, target in calc.DIRECTIONS:
        for workload in calc.BATCH_SIZES:
            batches = [calc.batch_result(b, groups[workload, b, source], groups[workload, b, target],
                                        (source, target), workload, frozen)
                       for b in range(1, calc.BATCHES + 1)]
            strata.append({"from": source, "to": target, "workload": workload,
                           "batch_size": calc.BATCH_SIZES[workload],
                           "primary": calc.summarize([b["primary"] for b in batches]),
                           "secondary_pooled_envelope": calc.summarize(
                               [b["secondary_pooled_envelope"] for b in batches]),
                           "cell_diagnostics": {label: calc.diagnostics(
                               [r for b in range(1, calc.BATCHES + 1) for r in groups[workload, b, model]])
                               for label, model in (("source", source), ("target", target))},
                           "batches": batches})
    return calc.json_safe({
        "schema": "fleetdiff-aggregate-analysis/v1", "frozen_product_version": "v0.10.0",
        "movement_fraction": calc.MOVE, "primary_target": "moved_only_cold_text_cost_usd",
        "prior_per_request_claim": "failed_closed", "planned_cells": len(cells),
        "all_workload_specific_gates_pass": all(s["primary"]["gates"]["pass"] for s in strata),
        "secondary_can_rescue_primary": False, "frozen_sha256": dict(PINS),
        "diagnostics": calc.diagnostics(cells), "strata": strata, "cells": cells,
        "limits": ["Fixed-batch empirical gates; not a future-traffic coverage guarantee.",
                   "No quality, individual-request, monthly-bill or deployment claim.",
                   "Unobserved errors are null; observed-error medians report their own n.",
                   "Full-scenario unchanged cost never enters primary width gates."]})


def reproduce():
    tasks = load_tasks()
    check_exclusions(tasks)
    return analyze(load_usage(tasks), load_frozen())


def serialized(result):
    return json.dumps(result, indent=2, sort_keys=True, allow_nan=False) + "\n"


def table(result):
    names = dict(zip(calc.MODELS, ("Sonnet", "Sol", "Haiku", "Luna")))
    workloads = {"qa": "QA", "extraction": "Extraction", "coding": "Coding"}
    def percent(value):
        return "inf" if value == "Infinity" else f"{Decimal(value) * 100:.1f}%"
    lines = ["| Direction | Workload | Covered / 20 | Wilson 95% | Width rank 10 / 18 | Median signed error USD | Median absolute error USD | Result |",
             "| --- | --- | ---: | --- | --- | ---: | ---: | --- |"]
    for stratum in result["strata"]:
        summary = stratum["primary"]
        wilson, errors = summary["wilson95"], summary["observed_errors_usd"]
        outcome = "Pass" if summary["gates"]["pass"] else "Fail"
        if summary["unavailable_n"]:
            outcome += "; incomplete"
        lines.append("| " + " | ".join((
            f"{names[stratum['from']]} -> {names[stratum['to']]}", workloads[stratum["workload"]],
            f"{summary['covered']}/20", f"{percent(wilson['lower'])}-{percent(wilson['upper'])}",
            f"{percent(summary['median_relative_width'])} / {percent(summary['p90_relative_width'])}",
            f"{Decimal(errors['median_signed_usd']):+.8f}",
            f"{Decimal(errors['median_absolute_usd']):.8f}", outcome)) + " |")
    return "\n".join(lines) + "\n"


def check(result):
    digest = hashlib.sha256(serialized(result).encode("ascii")).hexdigest()
    require(digest == (HERE / "expected-analysis.sha256").read_text().strip(),
            "full-precision analysis differs from expected output")
    content = PAGE.read_text(encoding="ascii")
    require(content.count(TABLE_START) == content.count(TABLE_END) == 1, "missing or duplicate table markers")
    actual = content.split(TABLE_START, 1)[1].split(TABLE_END, 1)[0].strip() + "\n"
    require(actual == table(result), "page table differs from regenerated analysis")
    diagnostic = result["diagnostics"]
    passed = sum(s["primary"]["gates"]["pass"] for s in result["strata"])
    return (f"Analysis reproduced: {len(result['strata'])} cases, {calc.BATCHES} planned batches each; {passed} pass.\n"
            f"Cells: {diagnostic['available_cells']} available / {diagnostic['planned_cells']} planned.\n"
            f"Full-precision SHA-256: {digest}\n"
            "Exact analysis and page table: PASS.\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    modes = parser.add_mutually_exclusive_group(required=True)
    modes.add_argument("--check", action="store_true")
    modes.add_argument("--json", action="store_true")
    modes.add_argument("--table", action="store_true")
    args = parser.parse_args()
    try:
        result = reproduce()
        output = serialized(result) if args.json else table(result) if args.table else check(result)
    except (ValueError, OSError, ArithmeticError, KeyError, TypeError):
        print("Reproduction refused: check scalar records, frozen inputs and expected output.", file=sys.stderr)
        return 2
    print(output, end="")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
