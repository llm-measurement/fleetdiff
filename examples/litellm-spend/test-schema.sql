-- SPDX-License-Identifier: Apache-2.0
-- Code authors: Vijay and Codex
-- Disposable test database only. Mirrors the pinned Prisma spend columns used
-- by this recipe, not LiteLLM's complete schema or migration machinery.
CREATE TABLE public."LiteLLM_SpendLogs" (
    request_id text PRIMARY KEY,
    call_type text NOT NULL,
    api_key text NOT NULL DEFAULT '',
    "user" text DEFAULT '',
    team_id text,
    organization_id text,
    end_user text,
    model text NOT NULL DEFAULT '',
    model_id text DEFAULT '',
    model_group text DEFAULT '',
    custom_llm_provider text DEFAULT '',
    prompt_tokens integer NOT NULL DEFAULT 0,
    completion_tokens integer NOT NULL DEFAULT 0,
    total_tokens integer NOT NULL DEFAULT 0,
    spend double precision NOT NULL DEFAULT 0,
    status text,
    session_id text,
    "startTime" timestamp(3) NOT NULL,
    "endTime" timestamp(3) NOT NULL,
    metadata jsonb DEFAULT '{}',
    messages jsonb DEFAULT '{}',
    response jsonb DEFAULT '{}',
    proxy_server_request jsonb DEFAULT '{}'
);
CREATE ROLE fixture_reader LOGIN;
GRANT CONNECT ON DATABASE fixture TO fixture_reader;
GRANT USAGE ON SCHEMA public TO fixture_reader;
GRANT SELECT ON public."LiteLLM_SpendLogs" TO fixture_reader;
