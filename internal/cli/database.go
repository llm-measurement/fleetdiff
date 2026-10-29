// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/exportdata"
	"github.com/llm-measurement/fleetdiff/internal/spend"
)

const exportHelp = `Export a private, read-only LiteLLM database snapshot.

Usage:
  fleetdiff export litellm --out NEW_DIRECTORY [options]

Options:
  --from UTC                 Inclusive UTC date or RFC3339 timestamp
  --to UTC                   Exclusive UTC date or RFC3339 timestamp
  --format csv|jsonl         Spend file format (default csv)
  --daily                    Include daily row counters
  --routing-details          Include original-route and agent details
  --request-tags             Include every request tag as private JSON-array text
  --usage-details=false      Omit default-on cache and usage details
  --database-url-file FILE   Read credentials from an owner-only regular file

The default range is the last 14 complete UTC days. Supply both bounds to
override it. Credentials otherwise come from FLEETDIFF_LITELLM_DATABASE_URL,
never command-line URLs. Remote databases require TLS with verify-full.
The output directory must be new; directory mode is 0700 and files are 0600.
Exports contain private identities. Keep them private.
Handled export cancellation cleans temporary data. Forced termination can leave
private temporary data behind.
`

type databaseFlags struct {
	enabled, daily, routing, usage bool
	requestTags                    bool
	keep, credentials              string
}

const databaseURLMessage = "URLs are not accepted in database command arguments; use FLEETDIFF_LITELLM_DATABASE_URL or --database-url-file"

func (d *databaseFlags) register(f *flag.FlagSet) {
	f.BoolVar(&d.enabled, "litellm-db", false, "")
	f.StringVar(&d.keep, "keep-export", "", "")
	f.StringVar(&d.credentials, "database-url-file", "", "")
	f.BoolVar(&d.daily, "daily", false, "")
	f.BoolVar(&d.routing, "routing-details", false, "")
	f.BoolVar(&d.usage, "usage-details", true, "")
	f.BoolVar(&d.requestTags, "request-tags", false, "")
}

func (d databaseFlags) validate(f *flag.FlagSet) error {
	var err error
	f.Visit(func(v *flag.Flag) {
		switch v.Name {
		case "keep-export", "database-url-file", "daily", "routing-details", "usage-details", "request-tags":
			if !d.enabled {
				err = errors.New("database export options require --litellm-db")
			} else if (v.Name == "keep-export" || v.Name == "database-url-file") && strings.TrimSpace(v.Value.String()) == "" {
				err = errors.New("database file and directory options must be nonempty")
			}
		case "litellm-spend", "litellm-daily-spend", "summaries", "expected", "model-usage-contract", "before", "after":
			if d.enabled {
				err = errors.New("choose --litellm-db or local inputs, not both")
			}
		}
	})
	return err
}

func databaseURLArgument(args []string) bool {
	for _, arg := range args {
		_, value, ok := strings.Cut(arg, "=")
		if !ok {
			value = arg
		}
		value = strings.ToLower(strings.TrimSpace(value))
		if strings.Contains(arg, "://") || strings.HasPrefix(value, "postgres:") || strings.HasPrefix(value, "postgresql:") {
			return true
		}
	}
	return false
}

func defaultDatabasePeriod(now time.Time) spend.Period {
	end := now.UTC().Truncate(24 * time.Hour)
	return spend.Period{Start: end.AddDate(0, 0, -14), End: end}
}

func periodText(p spend.Period) string {
	return p.Start.UTC().Format(time.RFC3339Nano) + "/" + p.End.UTC().Format(time.RFC3339Nano)
}

func databasePeriod(value string, now time.Time) (spend.Period, error) {
	if value == "" {
		return defaultDatabasePeriod(now), nil
	}
	p, err := spend.ParsePeriod(value)
	if err != nil || p.End.After(now) {
		return spend.Period{}, errors.New("database period must be a completed UTC date or RFC3339 start/end interval")
	}
	return p, nil
}

func databaseInvestigationPeriods(before, after string, now time.Time) (spend.Period, string, string, error) {
	if before == "" && after == "" {
		p := defaultDatabasePeriod(now)
		middle := p.Start.AddDate(0, 0, 7)
		return p, periodText(spend.Period{Start: p.Start, End: middle}), periodText(spend.Period{Start: middle, End: p.End}), nil
	}
	a, e1 := spend.ParsePeriod(before)
	b, e2 := spend.ParsePeriod(after)
	if e1 != nil || e2 != nil || a.End.After(b.Start) || a.End.Sub(a.Start) != b.End.Sub(b.Start) || b.End.After(now) {
		return spend.Period{}, "", "", errors.New("select two completed, nonoverlapping periods of equal duration")
	}
	return spend.Period{Start: a.Start, End: b.End}, before, after, nil
}

func validateDatabaseDaily(p spend.Period, daily bool) error {
	if daily && (!p.Start.Equal(p.Start.UTC().Truncate(24*time.Hour)) || !p.End.Equal(p.End.UTC().Truncate(24*time.Hour)) || p.End.Sub(p.Start) > 3660*24*time.Hour) {
		return errors.New("daily export needs complete UTC days spanning at most 3,660 days")
	}
	return nil
}

func databaseExportError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return errors.New("database export canceled")
	}
	var failure exportdata.Failure
	if errors.As(err, &failure) {
		return failure
	}
	return exportdata.ErrDatabase
}

// Keep a private parent because the exporter requires a destination that does
// not exist yet. Cleanup owns only that parent, never a user-supplied directory.
func (d databaseFlags) export(ctx context.Context, p spend.Period, exporter exportdata.Exporter) (exportdata.Result, func(), error) {
	cleanup := func() {}
	if exporter == nil {
		return exportdata.Result{}, cleanup, errors.New("database export is unavailable in this offline caller")
	}
	if ctx.Err() != nil {
		return exportdata.Result{}, cleanup, databaseExportError(ctx, ctx.Err())
	}
	directory := d.keep
	if directory == "" {
		var err error
		directory, cleanup, err = databaseTemporaryExport()
		if err != nil {
			return exportdata.Result{}, func() {}, err
		}
	}
	r, err := exporter(ctx, exportdata.Options{
		Directory: directory, CredentialFile: d.credentials, Start: p.Start, End: p.End,
		Format: "csv", Daily: d.daily, Routing: true, UsageDetails: d.usage,
		RequestTags: d.requestTags,
	})
	if err != nil || ctx.Err() != nil {
		cleanup()
		return exportdata.Result{}, func() {}, databaseExportError(ctx, err)
	}
	return r, cleanup, nil
}

func databaseTemporaryExport() (string, func(), error) {
	failure := errors.New("cannot create private temporary export directory under a trusted parent")
	// Resolve the system temporary alias before creating anything private.
	base, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return "", nil, failure
	}
	base, err = filepath.Abs(base)
	if err != nil {
		return "", nil, failure
	}
	parent, err := os.OpenRoot("/")
	if err != nil {
		return "", nil, failure
	}
	// Trust every ancestor, not just TMPDIR: an unprotected writable ancestor
	// could replace the private directory's name before the exporter sees it.
	for _, part := range append([]string{"."}, strings.FieldsFunc(base, func(r rune) bool { return r == '/' })...) {
		info, err := parent.Lstat(part)
		if err != nil || !info.IsDir() {
			parent.Close()
			return "", nil, failure
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 && stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0 {
			parent.Close()
			return "", nil, failure
		}
		next, err := parent.OpenRoot(part)
		parent.Close()
		if err != nil {
			return "", nil, failure
		}
		opened, err := next.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			next.Close()
			return "", nil, failure
		}
		parent = next
	}
	name := "fleetdiff-export-" + rand.Text()
	if err := parent.Mkdir(name, 0700); err != nil {
		parent.Close()
		return "", nil, failure
	}
	info, err := parent.Lstat(name)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		parent.Close()
		return "", nil, failure
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		parent.Close()
		return "", nil, failure
	}
	opened, err := root.Stat(".")
	stat, owned := info.Sys().(*syscall.Stat_t)
	if err != nil || !os.SameFile(info, opened) || !owned || stat.Uid != uint32(os.Geteuid()) {
		root.Close()
		parent.Close()
		return "", nil, failure
	}
	path := filepath.Join(base, name)
	cleanup := func() {
		if root == nil {
			return
		}
		// Recursion is confined to the known export child of the held private
		// root. Never recursively remove its replaceable public pathname.
		_ = root.RemoveAll("export")
		root.Close()
		root = nil
		if current, err := parent.Lstat(name); err == nil && os.SameFile(info, current) {
			// Rmdir cannot delete a replacement file, symlink, or nonempty tree.
			_ = syscall.Rmdir(path)
		}
		parent.Close()
	}
	return filepath.Join(path, "export"), cleanup, nil
}

func runExport(ctx context.Context, args []string, out, errout io.Writer, exporter exportdata.Exporter) int {
	fail := func(code int, message string) int { fmt.Fprintln(errout, message); return code }
	if databaseURLArgument(args) {
		return fail(2, databaseURLMessage)
	}
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		if _, err := io.WriteString(out, exportHelp); err != nil {
			return fail(1, "cannot write export help")
		}
		return 0
	}
	if args[0] != "litellm" {
		return fail(2, "export supports litellm; run fleetdiff export --help")
	}
	f := flag.NewFlagSet("export litellm", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	var opts exportdata.Options
	var from, to string
	f.StringVar(&opts.Directory, "out", "", "")
	f.StringVar(&opts.CredentialFile, "database-url-file", "", "")
	f.StringVar(&from, "from", "", "")
	f.StringVar(&to, "to", "", "")
	f.StringVar(&opts.Format, "format", "csv", "")
	f.BoolVar(&opts.Daily, "daily", false, "")
	f.BoolVar(&opts.Routing, "routing-details", false, "")
	f.BoolVar(&opts.UsageDetails, "usage-details", true, "")
	f.BoolVar(&opts.RequestTags, "request-tags", false, "")
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			if _, err := io.WriteString(out, exportHelp); err != nil {
				return fail(1, "cannot write export help")
			}
			return 0
		}
		return fail(2, "invalid export options; run fleetdiff export --help")
	}
	empty := false
	f.Visit(func(v *flag.Flag) {
		if v.Name == "from" || v.Name == "to" || v.Name == "database-url-file" {
			empty = empty || strings.TrimSpace(v.Value.String()) == ""
		}
	})
	if f.NArg() != 0 || strings.TrimSpace(opts.Directory) == "" || empty || opts.Format != "csv" && opts.Format != "jsonl" || (from == "") != (to == "") {
		return fail(2, "export needs a new output directory, csv or jsonl, and both UTC bounds when supplied")
	}
	p := defaultDatabasePeriod(time.Now())
	if from != "" {
		bounds := []*time.Time{&p.Start, &p.End}
		for i, value := range []string{from, to} {
			layout := time.RFC3339Nano
			if len(value) == 10 {
				layout = time.DateOnly
			}
			t, err := time.Parse(layout, value)
			_, offset := t.Zone()
			if err != nil || offset != 0 || t.Year() < 1970 || t.Year() > 2200 {
				return fail(2, "export bounds must be UTC dates or RFC3339 timestamps between 1970 and 2200")
			}
			*bounds[i] = t.UTC()
		}
		if !p.Start.Before(p.End) || p.End.After(time.Now()) {
			return fail(2, "export needs an increasing, completed UTC interval")
		}
	}
	opts.Start, opts.End = p.Start, p.End
	if err := validateDatabaseDaily(p, opts.Daily); err != nil {
		return fail(2, err.Error())
	}
	if exporter == nil {
		return fail(1, "database export is unavailable in this offline caller")
	}
	if ctx.Err() != nil {
		return fail(1, databaseExportError(ctx, ctx.Err()).Error())
	}
	r, err := exporter(ctx, opts)
	if err != nil || ctx.Err() != nil {
		return fail(1, databaseExportError(ctx, err).Error())
	}
	if _, err = fmt.Fprintf(out, "Exported %d spend rows (%s to %s, end exclusive). Keep the export private.\n", r.Rows, opts.Start.Format(time.RFC3339Nano), opts.End.Format(time.RFC3339Nano)); err != nil {
		return fail(1, "cannot write export result")
	}
	return 0
}
