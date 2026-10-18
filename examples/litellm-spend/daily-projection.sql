-- SPDX-License-Identifier: Apache-2.0
-- Code authors: Vijay and Codex
-- Included by export.sql inside its read-only, repeatable-read transaction.
-- Sum all user/model/key rows. Do not export identifiers or synthesize absent days.
COPY (
  SELECT date,
         sum(api_requests)::bigint AS api_requests,
         sum(successful_requests)::bigint AS successful_requests,
         sum(failed_requests)::bigint AS failed_requests
  FROM public."LiteLLM_DailyUserSpend"
  WHERE date >= to_char(:'start_utc'::timestamptz, 'YYYY-MM-DD')
    AND date < to_char(:'end_utc'::timestamptz, 'YYYY-MM-DD')
  GROUP BY date
  ORDER BY date COLLATE "C"
) TO STDOUT WITH (FORMAT CSV, HEADER true)
\g :daily_csv_file
