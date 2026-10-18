// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package postgres

import (
	"errors"
	"strings"
	"time"

	litellmsql "github.com/llm-measurement/fleetdiff/examples/litellm-spend"
	"github.com/llm-measurement/fleetdiff/internal/exportdata"
)

var errProjection = errors.New("embedded LiteLLM projection is invalid")

func recipeBlock(marker string) (string, error) {
	parts := strings.Split(litellmsql.Recipe, marker)
	if len(parts) != 3 {
		return "", errProjection
	}
	return parts[1], nil
}

// COPY has no bind parameters. Only UTC timestamps formatted from checked
// time.Time values and fixed, embedded fragments may enter its SQL text.
func queries(o exportdata.Options) (string, string, error) {
	if o.Start.IsZero() || o.Start.Year() < 1970 || o.End.Year() > 2200 || !o.Start.Before(o.End) {
		return "", "", errors.New("export needs ascending UTC bounds between 1970 and 2200")
	}
	query, err := recipeBlock("$projection$")
	if err != nil {
		return "", "", err
	}
	details, err := recipeBlock("$details$")
	if err != nil {
		return "", "", err
	}
	routing, err := recipeBlock("$routing$")
	if err != nil {
		return "", "", err
	}
	if !o.UsageDetails {
		details = ""
	}
	if !o.Routing {
		routing = ""
	}
	if strings.Count(query, "%s") != 1 || strings.Count(query, "%L") != 2 {
		return "", "", errProjection
	}
	query = strings.Replace(query, "%s", details+routing, 1)
	query = strings.Replace(query, "%L", sqlTime(o.Start), 1)
	query = strings.Replace(query, "%L", sqlTime(o.End), 1)
	daily := litellmsql.DailyRecipe
	daily = strings.ReplaceAll(daily, ":'start_utc'", sqlTime(o.Start))
	daily = strings.ReplaceAll(daily, ":'end_utc'", sqlTime(o.End))
	index := strings.Index(daily, "\n\\g ")
	if index < 0 {
		return "", "", errProjection
	}
	daily = daily[:index]
	return query, daily, nil
}

func sqlTime(t time.Time) string { return "'" + t.UTC().Format(time.RFC3339Nano) + "'" }
