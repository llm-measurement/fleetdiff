# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Fresh deterministic OTLP examples, never copied from native captures."""
import argparse
import copy
from datetime import datetime, timezone
import json
import os
from pathlib import Path

SENTINEL = "SYNTHETIC_PRIVATE_CONTENT_DO_NOT_CAPTURE"
IDENTITY = {"user.account_uuid": "synthetic-account-a", "user.email": "account-a@example.invalid",
            "user.id": "synthetic-installation-a", "session.id": "synthetic-session-a"}


def value(item):
    if isinstance(item, str):
        return {"stringValue": item}
    if isinstance(item, int):
        return {"intValue": str(item)}
    if isinstance(item, float):
        return {"doubleValue": item}
    return {"kvlistValue": {"values": attributes(item)}}


def attributes(items):
    return [{"key": key, "value": value(item)} for key, item in items.items()]


def record(event="api_request", sequence=5, day=11):
    fields = {"event.name": event, "event.sequence": sequence,
              **IDENTITY, "prompt.id": "synthetic-prompt-a"}
    if event == "api_request":
        fields.update(input_tokens=7, output_tokens=4, cache_read_tokens=11,
                      cache_creation_tokens=5, cost_usd=0.00003435, cost_usd_micros=34,
                      model="synthetic-model-a")
    else:
        fields.update(status_code=429, **({"attempt": 3} if event == "api_error" else {"total_attempts": 3}))
    return {"timeUnixNano": str(int(datetime(2026, 10, day, tzinfo=timezone.utc).timestamp()) * 1000000000),
            "body": {"stringValue": "claude_code." + event}, "attributes": attributes(fields)}


def envelope(records):
    return {"resourceLogs": [{"resource": {"attributes": attributes({
        "service.name": "claude-code", "service.version": "2.1.296",
        "fleetdiff.capture.profile": "claude-code-logs/v1", **IDENTITY})},
        "scopeLogs": [{"scope": {"name": "synthetic-generator"}, "logRecords": records}]}]}


def clean():
    return envelope([record(day=11), record(sequence=8, day=12),
                     record("api_error", 9, 12), record("api_retries_exhausted", 10, 12)])


def poisoned():
    request = clean()
    resource = request["resourceLogs"][0]
    resource["schemaUrl"] = "https://example.invalid/" + SENTINEL
    resource["resource"]["attributes"] += attributes({
        "process.command_line": SENTINEL, "unknown.resource": {"nested": SENTINEL}})
    resource["resource"]["attributes"][0]["value"] = value(SENTINEL)
    resource["resource"]["attributes"][2]["value"] = value(SENTINEL)
    scope = resource["scopeLogs"][0]
    scope["schemaUrl"] = "https://example.invalid/scope/" + SENTINEL
    scope["scope"] = {"name": SENTINEL, "version": SENTINEL,
                      "attributes": attributes({"unknown.scope": SENTINEL})}
    for log in scope["logRecords"]:
        log["eventName"] = SENTINEL
        log["severityText"] = SENTINEL
        log["traceId"] = "1234567890abcdef1234567890abcdef"
        log["spanId"] = "1234567890abcdef"
        log["attributes"] = [item for item in log["attributes"] if item["key"] != "model"]
        log["attributes"] += attributes({"error": SENTINEL, "prompt": SENTINEL,
                                         "model": SENTINEL, "query_source": SENTINEL,
                                         "unknown.attr": {"code": SENTINEL}})
    for body in (value(SENTINEL), value({"code": SENTINEL}), {"arrayValue": {"values": [value(SENTINEL)]}}):
        unknown = record()
        unknown["body"] = body
        scope["logRecords"].append(unknown)
    unknown = record("user_prompt")
    unknown["attributes"] += attributes({"prompt": SENTINEL})
    scope["logRecords"].append(unknown)
    mismatch = record()
    mismatch["body"] = value("claude_code.api_error")
    scope["logRecords"].append(mismatch)
    return request


def malformed():
    request = envelope([record()])
    resource = request["resourceLogs"][0]
    resource["resource"]["attributes"] = attributes({"service.version": SENTINEL, "user.email": {"code": SENTINEL}})
    log = resource["scopeLogs"][0]["logRecords"][0]
    log["attributes"] = attributes({"event.name": "api_request", "input_tokens": SENTINEL,
        "output_tokens": {"code": SENTINEL}, "cost_usd": SENTINEL, "cost_usd_micros": SENTINEL,
        "event.sequence": SENTINEL, "user.id": {"code": SENTINEL}})
    return request


def conflict():
    request = copy.deepcopy(clean())
    resource = request["resourceLogs"][0]
    resource["resource"]["attributes"] += attributes({"irrelevant": SENTINEL})
    for item in resource["resource"]["attributes"]:
        if item["key"] == "user.account_uuid":
            item["value"] = value("synthetic-conflicting-owner")
    return request


def unverified_identity_cases():
    cases = []
    for location in ("resource", "log"):
        for key in ("identity.source", "user.account_uuid", "user.email", "user.id"):
            for original in ({}, value(42), value({"nested": SENTINEL}), value(""), value(" \t")):
                sequence = 100 + len(cases)
                request = envelope([record(sequence=sequence)])
                resource = request["resourceLogs"][0]
                target = resource["resource"] if location == "resource" else resource["scopeLogs"][0]["logRecords"][0]
                target["attributes"] = [item for item in target["attributes"] if item["key"] != key]
                target["attributes"].append({"key": key, "value": original})
                cases.append((sequence, location, key, request))
        request = envelope([record(sequence=100 + len(cases))])
        resource = request["resourceLogs"][0]
        target = resource["resource"] if location == "resource" else resource["scopeLogs"][0]["logRecords"][0]
        target["attributes"] += attributes({"identity.source": SENTINEL})
        cases.append((100 + len(cases), location, "identity.source", request))
    return cases


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--variant", choices=("clean", "poisoned", "malformed", "conflict"), default="clean")
    parser.add_argument("--output", type=Path, help="new synthetic file; otherwise print JSON")
    args = parser.parse_args()
    encoded = json.dumps(globals()[args.variant](), separators=(",", ":")) + "\n"
    if args.output:
        with args.output.open("x", encoding="ascii") as output:
            os.fchmod(output.fileno(), 0o600)
            output.write(encoded)
    else:
        print(encoded, end="")
