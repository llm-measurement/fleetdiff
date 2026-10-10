# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Run only inside the pinned image, offline. Emit synthetic writer payloads."""
from contextlib import redirect_stderr, redirect_stdout
from datetime import datetime, timedelta, timezone
import hashlib
from importlib.metadata import version
import json
import os
from pathlib import Path
import sys

from generate import COLUMNS, ROOT


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def native_response(call_type, cached, trace_id):
    """Exercise the pinned logging normalizers, then pass their output to DB writer."""
    from litellm.litellm_core_utils.litellm_logging import Logging
    from litellm.types.llms.openai import ResponsesAPIResponse

    # The pinned Messages adapters set the logging operation to the canonical
    # name. Exercise both stored writer spellings with that normalized usage.
    context = Logging(model="claude-sonnet-4-5" if "anthropic" in call_type else "gpt-4.1",
                      messages=[], stream=False,
                      call_type="anthropic_messages" if "anthropic" in call_type else call_type,
                      start_time=datetime(2026, 10, 7, tzinfo=timezone.utc),
                      litellm_call_id="synthetic-native", function_id="synthetic-native",
                      litellm_trace_id=trace_id)
    context.optional_params = {}
    if "anthropic" in call_type:
        raw = {"id": "synthetic-native-message", "type": "message", "role": "assistant",
               "model": context.model, "content": [{"type": "text", "text": "synthetic"}],
               "stop_reason": "end_turn", "stop_sequence": None,
               "usage": {"input_tokens": 100, "output_tokens": 10,
                         "cache_read_input_tokens": 80 if cached else 0,
                         "cache_creation_input_tokens": 20 if cached else 0}}
    else:
        raw = ResponsesAPIResponse(id="synthetic-native-response", object="response",
            created_at=0, status="completed", model="gpt-4.1", output=[],
            parallel_tool_calls=False, tool_choice="auto", tools=[],
            usage={"input_tokens": 200 if cached else 100, "output_tokens": 10,
                   "total_tokens": 210 if cached else 110,
                   "input_tokens_details": {"cached_tokens": 80 if cached else 0},
                   "output_tokens_details": {"reasoning_tokens": 5}})
    _, _, result = context._success_handler_helper_fn(
        result=raw, end_time=datetime(2026, 10, 7, 0, 0, 1, tzinfo=timezone.utc))
    return result, context.model_call_details["standard_logging_object"]


def payloads():
    import litellm
    from litellm.proxy.spend_tracking import spend_tracking_utils as writer

    pins = json.loads((ROOT / "images.json").read_text())
    require(version("litellm") == pins["litellm_version"], "writer version mismatch")
    require(hashlib.sha256(Path(writer.__file__).read_bytes()).hexdigest() ==
            pins["writer_sha256"], "writer source mismatch")
    require(hashlib.sha256(Path("/app/schema.prisma").read_bytes()).hexdigest() ==
            pins["schema_sha256"], "writer schema mismatch")
    base = json.loads((ROOT / "synthetic.json").read_text())
    # Preserve the original inputs. Missing/explicit-zero usage collapse to the
    # same stock writer output; select one of each among the zero-only rows.
    cases = [(r["request_id"], r, "complete") for r in base]
    cases[6] = (base[6]["request_id"], base[6], "missing")
    for day in (7, 8):
        for mode in ("failure_partial", "input_only", "fallback", "missing_marker"):
            row = dict(base[0], request_id=f"synthetic-{day}-{mode}",
                       startTime=f"2026-10-{day:02}T18:00:00Z",
                       endTime=f"2026-10-{day:02}T18:00:01Z")
            cases.append((row["request_id"], row, mode))
        for operation in ("completion", "text_completion", "atext_completion",
                          "embedding", "aresponses", "agent", "tool"):
            row = dict(base[0], request_id=f"synthetic-{day}-{operation}",
                       call_type=operation, startTime=f"2026-10-{day:02}T19:00:00Z",
                       endTime=f"2026-10-{day:02}T19:00:01Z")
            cases.append((row["request_id"], row, "complete"))
        for operation in ("anthropic_messages", "aanthropic_messages", "responses", "aresponses"):
            for mode in ("native_cached", "native_uncached", "missing", "failure_partial"):
                row = dict(base[0], request_id=f"synthetic-{day}-{operation}-{mode}",
                           call_type=operation, startTime=f"2026-10-{day:02}T20:00:00Z",
                           endTime=f"2026-10-{day:02}T20:00:01Z")
                cases.append((row["request_id"], row, mode))
    for label, stamp in (
        ("outside_before", "2026-10-06T23:59:59.999Z"),
        ("at_start", "2026-10-07T00:00:00Z"),
        ("at_split", "2026-10-08T00:00:00Z"),
        ("outside_end", "2026-10-09T00:00:00Z"),
    ):
        row = dict(base[0], request_id="synthetic-" + label,
                   startTime=stamp, endTime=stamp)
        cases.append((row["request_id"], row, "complete"))

    result = []
    for label, row, mode in cases:
        metadata = {
            "user_api_key": row["api_key"],
            "user_api_key_user_id": row["user"],
            "user_api_key_team_id": row["team_id"],
            "user_api_key_org_id": "SENTINEL_ORG_73b9",
            "user_api_key_end_user_id": row["end_user"],
            "model_info": {"id": "SENTINEL_MODEL_ID_73b9"},
            "model_group": "SENTINEL_MODEL_GROUP_73b9",
            "session_id": "SENTINEL_METADATA_SESSION_73b9",
            "status": "failure" if mode == "failure_partial" else row["status"],
        }
        kwargs = {
            "call_type": row["call_type"], "model": row["model"],
            "litellm_call_id": label, "litellm_trace_id": row["session_id"],
            "response_cost": 0.0, "custom_llm_provider": "openai",
            "litellm_params": {"metadata": metadata},
        }
        usage = {name: row[name] for name in
                 ("prompt_tokens", "completion_tokens", "total_tokens")}
        response = {"usage": usage}
        expected = tuple(usage.values())
        if mode in ("native_cached", "native_uncached"):
            response, standard = native_response(row["call_type"], mode == "native_cached", row["session_id"])
            kwargs["standard_logging_object"] = standard
            response.id = label
            expected = (200, 10, 210) if mode == "native_cached" else (100, 10, 110)
            kwargs["custom_llm_provider"] = "anthropic" if "anthropic" in row["call_type"] else "openai"
        if label == "synthetic-000":
            usage["prompt_tokens_details"] = {"cached_tokens": 25, "cache_write_tokens": 10}
            usage["completion_tokens_details"] = {"reasoning_tokens": 5}
        if mode in ("missing", "failure_partial", "fallback", "missing_marker"):
            response = {}
            expected = (0, 0, 0)
        if mode == "failure_partial":
            kwargs["combined_usage_object"] = litellm.Usage(
                prompt_tokens=100, completion_tokens=3, total_tokens=103)
            expected = (100, 3, 103)
        elif mode == "input_only":
            response = {"usage": {"prompt_tokens": 100}}
            expected = (100, 0, 0)
        elif mode == "fallback":
            kwargs["standard_logging_object"] = {
                "metadata": {}, "model_map_information": {},
                "prompt_tokens": 17, "completion_tokens": 3, "total_tokens": 20,
            }
            expected = (17, 3, 20)
        elif mode == "missing_marker":
            metadata["usage_missing"] = True

        start = datetime.fromisoformat(row["startTime"].replace("Z", "+00:00"))
        # An offset-aware input must land as explicit UTC in the SQL export.
        if label == "synthetic-at_split":
            start = start.astimezone(timezone(timedelta(hours=-4)))
        end = datetime.fromisoformat(row["endTime"].replace("Z", "+00:00"))
        payload = writer.get_logging_payload(kwargs, response, start, end)
        require(tuple(payload.get(k) for k in
                      ("prompt_tokens", "completion_tokens", "total_tokens")) == expected,
                "writer usage contract changed")
        require(payload["status"] == metadata["status"], "writer status contract changed")
        require(payload["session_id"] == row["session_id"], "writer trace precedence changed")
        require(json.loads(payload["metadata"]).get("usage_missing") is None,
                "writer provenance contract changed")
        if label == "synthetic-000":
            details = json.loads(payload["metadata"])["additional_usage_values"]
            require(details.get("cache_read_input_tokens") == 25 and
                    details.get("cache_creation_input_tokens") == 10 and
                    details.get("completion_tokens_details", {}).get("reasoning_tokens") == 5,
                    "writer optional usage paths changed")
        if mode == "native_cached":
            details = json.loads(payload["metadata"])["additional_usage_values"]
            require(details.get("cache_read_input_tokens") == 80,
                    "native cache-read field changed")
            if "anthropic" in row["call_type"]:
                require(details.get("cache_creation_input_tokens") == 20,
                        "native cache-write field changed")
        projected = {k: payload.get(k) for k in COLUMNS}
        for name in ("startTime", "endTime"):
            projected[name] = payload[name].astimezone(timezone.utc).isoformat()
        # Retain actual writer metadata/content in the test DB. SQL must omit it.
        for name in ("metadata", "messages", "response", "proxy_server_request"):
            projected[name] = payload.get(name)
        result.append(projected)
    return result


def main():
    # Library import/log failures can contain source values. Never replay them.
    with open(os.devnull, "w") as sink, redirect_stdout(sink), redirect_stderr(sink):
        try:
            result = payloads()
        except Exception:
            result = None
    if result is None:
        sys.stderr.write("pinned synthetic writer probe failed\n")
        return 1
    json.dump(result, sys.stdout, separators=(",", ":"))
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
