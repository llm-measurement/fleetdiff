// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/llm-measurement/fleetdiff/internal/exportdata"
)

func TestDatabaseFailureClassifications(t *testing.T) {
	for _, item := range []struct{ in, want error }{
		{&pgproto3.ExceededMaxBodyLenErr{MaxExpectedBodyLen: 10, ActualBodyLen: 11}, errLimit},
		{fmt.Errorf("SENTINEL: %w", errLimit), errLimit},
		{&pgconn.PgError{Code: "57014", Message: "SENTINEL statement"}, errInterrupted},
		{&pgconn.PgError{Code: "55P03", Detail: "SENTINEL relation"}, errInterrupted},
		{&pgconn.PgError{Code: "25P03", Message: "SENTINEL idle transaction"}, errInterrupted},
		{context.Canceled, errInterrupted},
		{context.DeadlineExceeded, errInterrupted},
		{&pgconn.PgError{Code: "42P01", Message: "SENTINEL table"}, errDatabase},
		{errors.New("SENTINEL connection string"), errDatabase},
		{fmt.Errorf("SENTINEL: %w", exportdata.ErrStorage), exportdata.ErrStorage},
	} {
		if got := databaseFailure(item.in); got != item.want {
			t.Errorf("got %v, want %v", got, item.want)
		}
	}
}

type failedOutput struct{ short bool }

func (w failedOutput) Write(p []byte) (int, error) {
	if w.short {
		return len(p) - 1, nil
	}
	return 0, errors.New("SENTINEL_OUTPUT_PATH")
}

func TestOutputStorageFailureIsSafe(t *testing.T) {
	for _, short := range []bool{false, true} {
		w := byteLimit{w: failedOutput{short: short}, remaining: 100}
		if _, err := w.Write([]byte("fixture")); err != exportdata.ErrStorage {
			t.Fatal("storage failure was not classified safely")
		}
	}
}
