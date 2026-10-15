-- SPDX-License-Identifier: Apache-2.0
-- Code authors: Vijay and Codex
-- psql -Xq -v ON_ERROR_STOP=1; see README.md for bounds and output variables.
\set ON_ERROR_STOP on
\set QUIET on
\pset format unaligned
\pset tuples_only on
\if :{?usage_details}
\else
\set usage_details true
\endif
BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL TIME ZONE 'UTC';
SET LOCAL search_path = pg_catalog;
SET LOCAL statement_timeout = '120s';
SET LOCAL lock_timeout = '5s';

-- Require explicit UTC RFC3339 bounds, ascending and half-open.
SELECT 1 / CASE WHEN
  :'start_utc' ~ '^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|\+00:00)$'
  AND :'end_utc' ~ '^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|\+00:00)$'
  AND :'start_utc'::timestamptz < :'end_utc'::timestamptz
  THEN 1 ELSE 0 END AS valid_bounds
\gset

-- Optional coverage is invoked by export-coverage.sh using distinct inherited
-- descriptors. Do not accept arbitrary filenames that might alias each other.
\if :{?daily_csv_file}
SELECT 1 / CASE WHEN
  :'start_utc'::timestamptz = date_trunc('day', :'start_utc'::timestamptz)
  AND :'end_utc'::timestamptz = date_trunc('day', :'end_utc'::timestamptz)
  AND :'end_utc'::timestamptz - :'start_utc'::timestamptz <= interval '3660 days'
  THEN 1 ELSE 0 END AS valid_daily_bounds
\gset
\if :{?jsonl_file}
-- The guarded helper exports CSV only.
SELECT 1 / 0 AS unsupported_daily_jsonl
\gset
\else
SELECT 1 / CASE WHEN
  :'daily_csv_file' ~ '^/dev/fd/[0-9]+$'
  AND :'csv_file' ~ '^/dev/fd/[0-9]+$'
  AND substring(:'daily_csv_file' FROM 9)::bigint <> substring(:'csv_file' FROM 9)::bigint
  THEN 1 ELSE 0 END AS separate_daily_file
\gset
\endif
\endif

-- Default-on, pinned-writer paths only; usage_details=false omits these columns.
-- Missing details stay NULL, never zero.
SELECT CASE WHEN :'usage_details'::boolean THEN $details$
       , (metadata #>> '{additional_usage_values,cache_read_input_tokens}')::bigint
           AS cache_read_input_tokens
       , (metadata #>> '{additional_usage_values,cache_creation_input_tokens}')::bigint
           AS cache_write_input_tokens
       , (metadata #>> '{additional_usage_values,completion_tokens_details,reasoning_tokens}')::bigint
           AS reasoning_output_tokens
$details$ ELSE '' END AS detail_projection
\gset

-- Define the projection once, quoting bounds as SQL literals with format(%L).
-- Pinned Prisma DateTime columns are timestamp(3) without time zone, stored UTC.
-- Do not copy this interpretation to a differently configured database.
SELECT format($projection$
  SELECT request_id, call_type, api_key, "user", team_id, organization_id,
         end_user, model, model_id, model_group, custom_llm_provider,
         prompt_tokens, completion_tokens, total_tokens, spend, status, session_id,
         agent_id,
         -- Pinned LiteLLM v1.104.0 failure-writer shapes only. Messages stay in DB.
         -- NULL means unrecognized evidence, not proof that no rejection occurred.
         CASE WHEN status = 'failure'
           AND metadata #>> '{error_information,error_rate_limit_category}' = 'litellm_rate_limit'
         THEN CASE
           WHEN metadata #>> '{error_information,error_code}' = '429'
             AND metadata #>> '{error_information,error_class}' = 'ProxyRateLimitError'
             AND metadata #>> '{error_information,error_rate_limit_type}' = 'budget'
             AND starts_with(metadata #>> '{error_information,error_message}',
               'litellm.RateLimitError: Session budget exceeded for session ')
             THEN 'session_budget'
           WHEN metadata #>> '{error_information,error_code}' = '429'
             AND metadata #>> '{error_information,error_class}' = 'ProxyRateLimitError'
             AND metadata #>> '{error_information,error_rate_limit_type}' = 'max_iterations'
             AND starts_with(metadata #>> '{error_information,error_message}',
               'litellm.RateLimitError: Max iterations exceeded for session ')
             THEN 'session_iterations'
           -- This pin's default BudgetExceededError uses 422, irrespective of
           -- its normalized_error label. Customized errors remain unrecognized.
           WHEN metadata #>> '{error_information,error_code}' = '422'
             AND metadata #>> '{error_information,error_class}' = 'BudgetExceededError'
             AND metadata #>> '{error_information,error_rate_limit_type}' = 'budget'
             AND starts_with(metadata #>> '{error_information,error_message}',
               'Budget has been exceeded! Current cost: ')
             THEN 'other_budget'
           ELSE NULL
         END ELSE NULL END AS rejection_kind,
         to_char("startTime", 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') AS "startTime",
         to_char("endTime", 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') AS "endTime"
         %s
  FROM public."LiteLLM_SpendLogs"
  WHERE "startTime" >= (%L::timestamptz AT TIME ZONE 'UTC')
    AND "startTime" < (%L::timestamptz AT TIME ZONE 'UTC')
$projection$, :'detail_projection', :'start_utc', :'end_utc') AS export_query
\gset

-- Write one client-local file in one pass; JSONL replaces the default CSV.
-- psql emits JSONL directly so COPY text escaping cannot alter JSON strings.
\if :{?jsonl_file}
SELECT row_to_json(r) FROM (:export_query) AS r
\g :jsonl_file
\else
COPY (:export_query) TO STDOUT WITH (FORMAT CSV, HEADER true)
\g :csv_file
\endif
\if :{?daily_csv_file}
\ir daily-projection.sql
\endif
COMMIT;
