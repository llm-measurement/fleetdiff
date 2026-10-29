// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package postgres

import (
	"context"
	"strconv"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/llm-measurement/fleetdiff/internal/exportdata"
)

var errSchema = exportdata.ErrSchema

func checkSchema(ctx context.Context, c *pgconn.PgConn, daily, requestTags bool) error {
	spend := map[string]uint32{}
	for _, name := range []string{"request_id", "call_type", "api_key", "user", "team_id", "organization_id", "end_user", "model", "model_id", "model_group", "custom_llm_provider", "status", "session_id", "agent_id"} {
		spend[name] = 25
	}
	for _, name := range []string{"prompt_tokens", "completion_tokens", "total_tokens"} {
		spend[name] = 23
	}
	spend["spend"], spend["metadata"], spend["startTime"], spend["endTime"] = 701, 3802, 1114, 1114
	if requestTags {
		spend["request_tags"] = 3802
	}
	notNull := map[string]bool{}
	for _, name := range []string{"request_id", "call_type", "api_key", "model", "prompt_tokens", "completion_tokens", "total_tokens", "spend", "startTime", "endTime"} {
		notNull[name] = true
	}
	if err := schemaColumns(ctx, c, `public."LiteLLM_SpendLogs"`, spend, notNull); err != nil {
		return err
	}
	r := c.ExecParams(ctx, `SELECT count(*) FROM pg_catalog.pg_index i JOIN pg_catalog.pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=i.indkey[0] WHERE i.indrelid='public."LiteLLM_SpendLogs"'::regclass AND i.indisprimary AND i.indnkeyatts=1 AND a.attname='request_id'`, nil, nil, nil, nil).Read()
	if r.Err != nil {
		return databaseFailure(r.Err)
	}
	if len(r.Rows) != 1 || len(r.Rows[0]) != 1 || string(r.Rows[0][0]) != "1" {
		return errSchema
	}
	if daily {
		return schemaColumns(ctx, c, `public."LiteLLM_DailyUserSpend"`, map[string]uint32{"date": 25, "api_requests": 20, "successful_requests": 20, "failed_requests": 20}, map[string]bool{"date": true, "api_requests": true, "successful_requests": true, "failed_requests": true})
	}
	return nil
}

func schemaColumns(ctx context.Context, c *pgconn.PgConn, table string, expected map[string]uint32, notNull map[string]bool) error {
	r := c.ExecParams(ctx, `SELECT a.attname, a.atttypid::text, a.atttypmod::text, a.attnotnull::text FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON c.oid=a.attrelid WHERE a.attrelid=pg_catalog.to_regclass($1) AND a.attnum>0 AND NOT a.attisdropped AND c.relkind IN ('r','p')`, [][]byte{[]byte(table)}, nil, nil, nil).Read()
	if r.Err != nil {
		return databaseFailure(r.Err)
	}
	seen := map[string]bool{}
	for _, row := range r.Rows {
		if len(row) != 4 {
			return errSchema
		}
		name := string(row[0])
		want, ok := expected[name]
		if !ok {
			continue
		}
		got, err := strconv.ParseUint(string(row[1]), 10, 32)
		if err != nil || got != uint64(want) || want == 1114 && string(row[2]) != "3" || string(row[3]) != strconv.FormatBool(notNull[name]) {
			return errSchema
		}
		seen[name] = true
	}
	if len(seen) != len(expected) {
		return errSchema
	}
	return nil
}
