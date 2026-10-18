"""Frozen aggregate cost arithmetic; stdlib only, no product/runtime imports."""

from collections import Counter
from decimal import Decimal, Inexact, localcontext
from fractions import Fraction
import statistics

MODELS = ("claude-sonnet-5-5", "gpt-6.1-sol", "claude-haiku-5-5", "gpt-6-luna")
DIRECTIONS = ((MODELS[0], MODELS[1]), (MODELS[0], MODELS[2]),
              (MODELS[0], MODELS[3]), (MODELS[1], MODELS[3]))
BATCH_SIZES = {"qa": 30, "extraction": 30, "coding": 24}
BATCHES = 20
MOVE = Decimal("0.5")
INF = Decimal("Infinity")
Z95 = Decimal("1.959963984540054")
PRECISION = 80
TOKEN_FIELDS = ("input_tokens", "ordinary_input_tokens", "cache_read_tokens",
                "cache_write_tokens", "output_tokens", "reasoning_tokens")
FLAGS = ("nonempty", "truncated", "refusal_marker", "refusal_lexical", "response_valid")
LIABILITY_FIELDS = ("reserved_micro_usd", "settled_with_contingency_micro_usd")


def require(condition, message):
    if not condition:
        raise ValueError(message)


def cold_cost(input_tokens, output_tokens, rates, input_factor=Decimal(1),
              output_factor=Decimal(1), cache_read=0, cache_write=0):
    """Cold-only form of frozen cost(); no rounding or reasoning-token addition."""
    require(cache_read == 0 and cache_write == 0, "cannot price cached usage as cold")
    with localcontext() as context:
        context.prec = PRECISION
        context.traps[Inexact] = True
        return (Decimal(input_tokens) * input_factor * rates["input"]
                + Decimal(output_tokens) * output_factor * rates["output"]) / Decimal(1000000)


def relative_width(lower, upper, actual):
    require(0 <= lower <= upper and actual >= 0, "invalid cost interval")
    span = Fraction(upper) - Fraction(lower)
    return span / Fraction(actual) if actual else (Fraction(0) if not span else INF)


def wilson95(covered, n=BATCHES):
    require(type(n) is int and type(covered) is int and 0 <= covered <= n and n > 0,
            "invalid Wilson counts")
    with localcontext() as context:
        context.prec = PRECISION
        p, size = Decimal(covered) / n, Decimal(n)
        z2 = Z95 * Z95
        denominator = 1 + z2 / size
        center = (p + z2 / (2 * size)) / denominator
        half = Z95 * (p * (1 - p) / size + z2 / (4 * size * size)).sqrt() / denominator
        return {"confidence": "0.95", "method": "Wilson_two_sided", "n": n,
                "lower": Decimal(0) if covered == 0 else max(Decimal(0), center - half),
                "upper": Decimal(1) if covered == n else min(Decimal(1), center + half)}


def observation(lower=None, upper=None, actual=None, point=None):
    if lower is None or upper is None or actual is None:
        return {"available": False, "covered": False, "relative_width": INF,
                "lower_usd": None, "upper_usd": None, "actual_usd": None,
                "point_usd": None, "signed_error_usd": None, "absolute_error_usd": None}
    with localcontext() as context:
        context.prec = PRECISION
        context.traps[Inexact] = True
        signed = point - actual if point is not None else None
    return {"available": True, "covered": lower <= actual <= upper,
            "relative_width": relative_width(lower, upper, actual),
            "lower_usd": lower, "upper_usd": upper, "actual_usd": actual,
            "point_usd": point, "signed_error_usd": signed,
            "absolute_error_usd": signed.copy_abs() if signed is not None else None}


def summarize(observations):
    require(len(observations) == BATCHES, "summary requires all twenty planned batches")
    widths = sorted(r["relative_width"] for r in observations)
    covered = sum(r["covered"] for r in observations)
    errors = [r["signed_error_usd"] for r in observations if r["signed_error_usd"] is not None]
    with localcontext() as context:
        context.prec = PRECISION
        context.traps[Inexact] = True
        error_summary = {"n": len(errors), "planned_n": BATCHES,
                         "median_signed_usd": statistics.median(errors) if errors else None,
                         "median_absolute_usd": statistics.median([abs(e) for e in errors]) if errors else None}
    coverage_pass = covered >= 16
    median_pass, p90_pass = widths[9] <= Fraction(1, 2), widths[17] <= 1
    return {"n": BATCHES, "covered": covered, "wilson95": wilson95(covered),
            "available_n": sum(r["available"] for r in observations),
            "unavailable_n": sum(not r["available"] for r in observations),
            "width_n": BATCHES, "median_width_rank": 10, "p90_width_rank": 18,
            "median_relative_width": widths[9], "p90_relative_width": widths[17],
            "zero_cost_batch_indices_1based": [i for i, r in enumerate(observations, 1)
                                                if r["available"] and r["actual_usd"] == 0],
            "observed_errors_usd": error_summary,
            "gates": {"coverage": coverage_pass, "median_width": median_pass,
                      "p90_width": p90_pass, "pass": coverage_pass and median_pass and p90_pass}}


def diagnostics(receipts):
    """Counts are overlapping diagnostics; global counts use unique planned cells."""
    reasons = Counter(reason for r in receipts for reason in r["reasons"])
    flag_counts = {k: sum(r["flags"][k] is True for r in receipts) for k in FLAGS}
    unknown_flags = {k: sum(r["flags"][k] is None for r in receipts) for k in FLAGS}
    token_sums = {k: sum(r["usage"][k] or 0 for r in receipts) for k in TOKEN_FIELDS}
    token_ns = {k: sum(r["usage"][k] is not None for r in receipts) for k in TOKEN_FIELDS}
    costs = [r["reported_list_price_usd"] for r in receipts if r["reported_list_price_usd"] is not None]
    with localcontext() as context:
        context.prec = PRECISION
        context.traps[Inexact] = True
        total = sum(costs, Decimal(0))
    return {"planned_cells": len(receipts), "available_cells": sum(r["available"] for r in receipts),
            "unavailable_cells": sum(not r["available"] for r in receipts),
            "response_invalid_cells": sum(r["flags"]["response_valid"] is False for r in receipts),
            "empty_cells": sum(r["flags"]["nonempty"] is False for r in receipts),
            "refusal_any_cells": sum(r["flags"]["refusal_marker"] is True or
                                      r["flags"]["refusal_lexical"] is True for r in receipts),
            "flag_true_counts": flag_counts, "flag_unknown_counts": unknown_flags,
            "unavailable_reason_counts": dict(sorted(reasons.items())),
            "reported_token_sums": token_sums, "reported_token_field_n": token_ns,
            "reported_list_price_usd_sum": total, "reported_list_price_n": len(costs),
            "unpriced_cells": len(receipts) - len(costs),
            "reported_liability_fields": {
                k: {"sum_micro_usd": sum(r["liability"][k] or 0 for r in receipts),
                    "n": sum(r["liability"][k] is not None for r in receipts)} for k in LIABILITY_FIELDS},
            "liability_scope": "separate receipt fields, not additive charges or reconstructed journal holds",
            "reported_price_scope": "receipt claims including invalid/cache/identity failures; not primary cost"}


def batch_result(batch, source_rows, target_rows, direction, workload, frozen):
    inputs, outputs, rates = frozen
    source, target = direction
    result = {"batch_1based": batch, "tasks": BATCH_SIZES[workload],
              "unavailable_source_sequences": [r["sequence"] for r in source_rows if not r["available"]],
              "unavailable_target_sequences": [r["sequence"] for r in target_rows if not r["available"]],
              "primary": observation(), "secondary_pooled_envelope": observation(),
              "source_token_totals": None, "target_token_totals": None,
              "source_cost_usd": None, "moved_source_cost_usd": None,
              "moved_level1_cost_usd": None, "full_scenario": None}
    # Never estimate a batch from a partial source or a complete-case subset.
    if result["unavailable_source_sequences"] or result["unavailable_target_sequences"]:
        return result
    source_i, source_o = (sum(r["usage"][k] for r in source_rows) for k in ("input_tokens", "output_tokens"))
    target_i, target_o = (sum(r["usage"][k] for r in target_rows) for k in ("input_tokens", "output_tokens"))
    with localcontext() as context:
        context.prec = PRECISION
        context.traps[Inexact] = True
        base = cold_cost(source_i, source_o, rates[source])
        actual = MOVE * cold_cost(target_i, target_o, rates[target])
        unmoved = (1 - MOVE) * base
        level1 = MOVE * cold_cost(source_i, source_o, rates[target], inputs[direction])
        variants = {w: {name: MOVE * cold_cost(source_i, source_o, rates[target], inputs[direction], factor)
                        for name, factor in outputs[source, target, w].items()} for w in BATCH_SIZES}
        v = variants[workload]
        result.update(source_token_totals={"input": source_i, "output_including_reasoning": source_o},
                      target_token_totals={"input": target_i, "output_including_reasoning": target_o},
                      source_cost_usd=base, moved_source_cost_usd=MOVE * base,
                      moved_level1_cost_usd=level1,
                      full_scenario={"unmoved_source_usd": unmoved, "level1_usd": unmoved + level1,
                                     "actual_usd": unmoved + actual,
                                     **{name + "_usd": unmoved + value for name, value in v.items()}})
    result["primary"] = observation(v["lower"], v["upper"], actual, v["point"])
    result["secondary_pooled_envelope"] = observation(
        min(v["lower"] for v in variants.values()), max(v["upper"] for v in variants.values()), actual)
    return result


def json_safe(value):
    if isinstance(value, Decimal):
        require(not value.is_nan() and value != -INF, "invalid output decimal")
        return format(value, "f")
    if isinstance(value, Fraction):
        with localcontext() as context:
            context.prec = PRECISION
            return format(Decimal(value.numerator) / Decimal(value.denominator), "f")
    if isinstance(value, dict):
        return {k: json_safe(v) for k, v in value.items()}
    if isinstance(value, (list, tuple)):
        return [json_safe(v) for v in value]
    return value
