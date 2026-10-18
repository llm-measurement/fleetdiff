// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/localfile"
)

const maxCoverageDays = 3660
const maxCoverageBytes = 1 << 20
const maxCoverageLine = 512

var errCoverageFile = errors.New("daily spend input must be a readable regular CSV file, not a symlink or special file")
var errCoverageCSV = errors.New("invalid daily spend CSV; use the four-column read-only daily SQL projection")
var errCoverageLimit = errors.New("daily spend coverage exceeds 3,660 days, 1 MiB or 512 bytes per line; select a shorter period")

// ValidateDailyExportRoot applies the same bounded coverage parser through an
// already-held export directory, without resolving its public pathname again.
func ValidateDailyExportRoot(root *os.Root, name string) error {
	if root == nil {
		return errCoverageFile
	}
	_, err := readDailyCountsRoot(root, name)
	return err
}

// RowCoverage compares records with a second accounting path. Matching counts
// do not establish that requests missing from both paths were recorded.
type RowCoverage struct {
	Reference string        `json:"reference"`
	Days      []CoverageDay `json:"days"`
}

type CoverageDay struct {
	Date               string  `json:"date_utc"`
	Status             string  `json:"status"`
	ExportedRows       uint64  `json:"exported_rows"`
	APIRequests        *uint64 `json:"api_requests"`
	SuccessfulRequests *uint64 `json:"successful_requests"`
	FailedRequests     *uint64 `json:"failed_requests"`
	MissingRows        *uint64 `json:"missing_rows"`
	completeDay        bool
}

type dailyCounts struct {
	api, successful, failed uint64
}

// The projection contains dates and counts only. Bound the whole file before
// parsing, then each physical CSV line before encoding/csv allocates fields.
func readDailyCounts(path string) (map[string]dailyCounts, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, errCoverageFile
	}
	defer root.Close()
	return readDailyCountsRoot(root, filepath.Base(path))
}

func readDailyCountsRoot(root *os.Root, name string) (map[string]dailyCounts, error) {
	data, err := localfile.Read(root, name, maxCoverageBytes)
	if err != nil {
		return nil, fmt.Errorf("daily spend input: %w", err)
	}
	return parseDailyCounts(data)
}

func parseDailyCounts(data []byte) (map[string]dailyCounts, error) {
	if len(data) > maxCoverageBytes {
		return nil, errCoverageLimit
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, maxCoverageLine+2), maxCoverageLine+2)
	columns := map[string]int{}
	counts := map[string]dailyCounts{}
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) > maxCoverageLine {
			return nil, errCoverageLimit
		}
		if len(line) == 0 {
			continue
		}
		r := csv.NewReader(bytes.NewReader(line))
		r.FieldsPerRecord = 4
		fields, err := r.Read()
		if err != nil {
			return nil, errCoverageCSV
		}
		if len(columns) == 0 {
			for i, name := range fields {
				switch name {
				case "date", "api_requests", "successful_requests", "failed_requests":
				default:
					return nil, errCoverageCSV
				}
				if _, duplicate := columns[name]; duplicate {
					return nil, errCoverageCSV
				}
				columns[name] = i
			}
			continue
		}
		date := fields[columns["date"]]
		d, err := time.Parse(time.DateOnly, date)
		if err != nil || d.Year() < 1970 || d.Year() > 2199 || d.Format(time.DateOnly) != date {
			return nil, errCoverageCSV
		}
		if _, duplicate := counts[date]; duplicate {
			return nil, errCoverageCSV
		}
		if len(counts) == maxCoverageDays {
			return nil, errCoverageLimit
		}
		var values [3]uint64
		for i, column := range []string{"api_requests", "successful_requests", "failed_requests"} {
			value := fields[columns[column]]
			if value == "" {
				return nil, errCoverageCSV
			}
			for _, c := range value {
				if c < '0' || c > '9' {
					return nil, errCoverageCSV
				}
			}
			values[i], err = strconv.ParseUint(value, 10, 63)
			if err != nil {
				return nil, errCoverageCSV
			}
		}
		counts[date] = dailyCounts{values[0], values[1], values[2]}
	}
	if scanner.Err() != nil {
		return nil, errCoverageLimit
	}
	if len(columns) != 4 {
		return nil, errCoverageCSV
	}
	return counts, nil
}

type coverageBuilder struct {
	days map[string]*CoverageDay
}

func newCoverage(path string, periods ...Period) (*coverageBuilder, error) {
	if path == "" {
		return nil, nil
	}
	counts, err := readDailyCounts(path)
	if err != nil {
		return nil, err
	}
	b := &coverageBuilder{days: map[string]*CoverageDay{}}
	for _, period := range periods {
		for day := period.Start.UTC().Truncate(24 * time.Hour); day.Before(period.End); day = day.AddDate(0, 0, 1) {
			date := day.Format(time.DateOnly)
			d, exists := b.days[date]
			if !exists {
				if len(b.days) == maxCoverageDays {
					return nil, errCoverageLimit
				}
				d = &CoverageDay{Date: date}
				b.days[date] = d
				if c, ok := counts[date]; ok {
					d.APIRequests, d.SuccessfulRequests, d.FailedRequests = &c.api, &c.successful, &c.failed
				}
			}
			d.completeDay = d.completeDay || (!period.Start.After(day) && !period.End.Before(day.AddDate(0, 0, 1)))
		}
	}
	return b, nil
}

func (b *coverageBuilder) add(start time.Time) {
	if b != nil {
		b.days[start.UTC().Format(time.DateOnly)].ExportedRows++
	}
}

func (b *coverageBuilder) result() *RowCoverage {
	if b == nil {
		return nil
	}
	r := &RowCoverage{Reference: "LiteLLM_DailyUserSpend", Days: []CoverageDay{}}
	for _, d := range b.days {
		switch {
		case !d.completeDay:
			d.Status = "partial_day"
		case d.APIRequests == nil:
			d.Status = "daily_counts_missing"
		case d.ExportedRows > *d.APIRequests:
			d.Status = "excess_rows"
		default:
			missing := *d.APIRequests - d.ExportedRows
			d.MissingRows = &missing
			d.Status = "matched"
			if missing > 0 {
				d.Status = "missing_rows"
			}
		}
		r.Days = append(r.Days, *d)
	}
	sort.Slice(r.Days, func(i, j int) bool { return r.Days[i].Date < r.Days[j].Date })
	return r
}
