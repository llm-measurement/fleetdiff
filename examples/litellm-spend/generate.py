# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Streaming synthetic fixture and scale generator; no importer or provider code."""
import argparse
import csv
from datetime import date, timedelta
import json
import os
from pathlib import Path
import sys


ROOT = Path(__file__).resolve().parent
COLUMNS = (
    "request_id", "call_type", "api_key", "user", "team_id", "organization_id",
    "end_user", "model", "model_id", "model_group", "custom_llm_provider",
    "prompt_tokens", "completion_tokens", "total_tokens", "spend", "status",
    "session_id", "startTime", "endTime",
)
TOKEN_COLUMNS = ("prompt_tokens", "completion_tokens", "total_tokens")


def rows(count=20, *, unique_keys=False, key_cardinality=None, days=None):
    """Repeat the reviewed 20-row fixture with unique request and session IDs."""
    base = json.loads((ROOT / "synthetic.json").read_text(encoding="ascii"))
    period_counts = {"2026-10-07": 0, "2026-10-08": 0}
    dates = [(date(2026, 9, 23) + timedelta(days=i)).isoformat()
             for i in range(days or 0)]
    for index in range(count):
        row = dict(base[index % len(base)])
        if index >= len(base):
            row["request_id"] = f"synthetic-scale-{index:09d}"
            row["session_id"] = f"synthetic-scale-session-{index:09d}"
        if unique_keys:
            row["api_key"] = f"synthetic-scale-key-{index:09d}"
        elif key_cardinality:
            day = row["startTime"][:10]
            key = period_counts[day] % key_cardinality
            row["api_key"] = f"synthetic-scale-key-{key:09d}"
            period_counts[day] += 1
        if dates:
            day = dates[min(index * len(dates) // count, len(dates) - 1)]
            row["startTime"] = day + "T12:00:00Z"
            row["endTime"] = day + "T12:00:01Z"
        yield row


def write_rows(stream, records, encoding):
    if encoding == "csv":
        writer = csv.DictWriter(stream, fieldnames=COLUMNS, lineterminator="\n")
        writer.writeheader()
        writer.writerows(records)
    elif encoding == "jsonl":
        for row in records:
            stream.write(json.dumps(row, separators=(",", ":")) + "\n")
    else:
        stream.write("[\n")
        separator = ""
        for row in records:
            stream.write(separator + json.dumps(row, separators=(",", ":")))
            separator = ",\n"
        stream.write("\n]\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rows", type=int, default=1_000_000)
    parser.add_argument("--format", choices=("csv", "json", "jsonl"), default="csv")
    parser.add_argument("--output", type=Path, help="new output file; default is stdout")
    parser.add_argument("--days", type=int, help="spread rows over 1..16 days starting 2026-09-23")
    keys = parser.add_mutually_exclusive_group()
    keys.add_argument("--unique-keys", action="store_true", help="one distinct api_key per row")
    keys.add_argument("--key-cardinality", type=int,
                      help="cycle a shared key pool independently in each period")
    args = parser.parse_args()
    if not 1 <= args.rows <= 10_000_001:
        parser.error("rows must be 1..10000001 (the final value tests rejection)")
    if args.key_cardinality is not None and not 1 <= args.key_cardinality <= 10_000_000:
        parser.error("key cardinality must be 1..10000000")
    if args.days is not None and not 1 <= args.days <= 16:
        parser.error("days must be 1..16")
    records = rows(args.rows, unique_keys=args.unique_keys, key_cardinality=args.key_cardinality,
                  days=args.days)
    if args.output is None:
        write_rows(sys.stdout, records, args.format)
    else:
        try:
            descriptor = os.open(args.output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        except OSError:
            parser.error("cannot create output; choose a new file in a writable directory")
        try:
            with os.fdopen(descriptor, "w", encoding="ascii", newline="") as stream:
                write_rows(stream, records, args.format)
        except (OSError, ValueError):
            args.output.unlink(missing_ok=True)
            parser.error("generation failed; incomplete output removed")


if __name__ == "__main__":
    main()
