// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package postgres

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/llm-measurement/fleetdiff/internal/exportdata"
	"github.com/llm-measurement/fleetdiff/internal/spend"
)

var errDatabase = exportdata.ErrDatabase
var errLimit = exportdata.ErrLimit
var errInterrupted = exportdata.ErrInterrupted

func Export(parent context.Context, o exportdata.Options) (result exportdata.Result, err error) {
	if o.Format != "csv" && o.Format != "jsonl" {
		return result, errors.New("export format must be csv or jsonl")
	}
	query, daily, err := queries(o)
	if err != nil {
		return result, err
	}
	if o.Daily && (!o.Start.Equal(o.Start.UTC().Truncate(24*time.Hour)) || !o.End.Equal(o.End.UTC().Truncate(24*time.Hour)) || o.End.Sub(o.Start) > 3660*24*time.Hour) {
		return result, errors.New("daily export needs complete UTC days spanning at most 3,660 days")
	}
	raw, err := credentials(o.CredentialFile)
	if err != nil {
		return result, err
	}
	config, err := connectionConfig(raw)
	raw = ""
	if err != nil {
		return result, err
	}
	dir, err := newOutput(o.Directory)
	if err != nil {
		return result, err
	}
	defer dir.close()
	ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
	defer cancel()
	defer func() {
		if err != nil && ctx.Err() != nil {
			err = errInterrupted
		}
	}()
	conn, err := pgconn.ConnectConfig(ctx, config)
	config.Password = ""
	if err != nil {
		return result, databaseFailure(err)
	}
	defer func() {
		closeCtx, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = conn.Close(closeCtx)
	}()
	if err = exec(ctx, conn, `BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL TIME ZONE 'UTC'; SET LOCAL search_path = pg_catalog;
SET LOCAL statement_timeout = '120s'; SET LOCAL lock_timeout = '5s';
SET LOCAL idle_in_transaction_session_timeout = '120s';
SET LOCAL client_encoding = 'UTF8';`); err != nil {
		return result, err
	}
	if err = checkSchema(ctx, conn, o.Daily); err != nil {
		return result, err
	}
	name := "spend." + o.Format
	file, err := dir.create(name)
	if err != nil {
		return result, err
	}
	// One extra row detects the cap; it is never published as a truncated export.
	query += " LIMIT " + strconv.Itoa(spend.MaxRows+1)
	w := &byteLimit{w: file, remaining: spend.MaxFileBytes}
	var count int64
	if o.Format == "csv" {
		var tag pgconn.CommandTag
		tag, err = conn.CopyTo(ctx, w, "COPY ("+query+") TO STDOUT WITH (FORMAT CSV, HEADER true)")
		count = tag.RowsAffected()
	} else {
		r := conn.ExecParams(ctx, "SELECT row_to_json(r) FROM ("+query+") AS r", nil, nil, nil, nil)
		for r.NextRow() {
			values := r.Values()
			count++
			if count > spend.MaxRows || len(values) != 1 || len(values[0])+1 > spend.MaxRowBytes {
				err = errLimit
				break
			}
			if _, err = w.Write(values[0]); err != nil {
				break
			}
			if _, err = w.Write([]byte{'\n'}); err != nil {
				break
			}
		}
		if err == nil {
			_, err = r.Close()
		}
	}
	if err != nil || count > spend.MaxRows {
		file.Close()
		if count > spend.MaxRows || errors.Is(err, errLimit) {
			return result, errLimit
		}
		return result, databaseFailure(err)
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return result, exportdata.ErrStorage
	}
	if err = file.Close(); err != nil {
		return result, exportdata.ErrStorage
	}
	if o.Daily {
		f, e := dir.create("daily.csv")
		if e != nil {
			return result, e
		}
		tag, e := conn.CopyTo(ctx, &byteLimit{w: f, remaining: 1 << 20}, daily)
		if e != nil || tag.RowsAffected() > 3660 {
			f.Close()
			if tag.RowsAffected() > 3660 {
				return result, errLimit
			}
			return result, databaseFailure(e)
		}
		if e = f.Sync(); e != nil {
			f.Close()
			return result, exportdata.ErrStorage
		}
		if e = f.Close(); e != nil {
			return result, exportdata.ErrStorage
		}
	}
	if err = exec(ctx, conn, "COMMIT"); err != nil {
		return result, err
	}
	// Reuse the same bounded readers as every offline command before publication.
	err = spend.ReadRoutingRoot(dir.root, name, func(spend.Row) error { return ctx.Err() })
	if err != nil {
		return result, errLimit
	}
	if o.Daily {
		if err = spend.ValidateDailyExportRoot(dir.root, "daily.csv"); err != nil {
			return result, errors.New("daily export is outside the supported coverage format or limits")
		}
	}
	if err = dir.publish(ctx); err != nil {
		return result, err
	}
	result = exportdata.Result{Directory: o.Directory, SpendPath: filepath.Join(o.Directory, name), Start: o.Start, End: o.End, Rows: count}
	if o.Daily {
		result.DailyPath = filepath.Join(o.Directory, "daily.csv")
	}
	return result, nil
}

type byteLimit struct {
	w         io.Writer
	remaining int64
}

func (w *byteLimit) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, errLimit
	}
	n, err := w.w.Write(p)
	w.remaining -= int64(n)
	if err != nil || n != len(p) {
		return n, exportdata.ErrStorage
	}
	return n, err
}

func exec(ctx context.Context, c *pgconn.PgConn, sql string) error {
	if _, err := c.Exec(ctx, sql).ReadAll(); err != nil {
		return databaseFailure(err)
	}
	return nil
}

func databaseFailure(err error) error {
	if errors.Is(err, exportdata.ErrStorage) {
		return exportdata.ErrStorage
	}
	var oversized *pgproto3.ExceededMaxBodyLenErr
	if errors.Is(err, errLimit) || errors.As(err, &oversized) {
		return errLimit
	}
	var server *pgconn.PgError
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.As(err, &server) && (server.Code == "57014" || server.Code == "55P03" || server.Code == "25P03") {
		return errInterrupted
	}
	return errDatabase
}
