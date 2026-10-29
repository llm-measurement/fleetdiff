// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package postgres

import (
	"strings"
	"testing"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/exportdata"
)

func TestRequestTagsProjectionOptIn(t *testing.T) {
	o := exportdata.Options{Start: time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)}
	for _, usage := range []bool{false, true} {
		for _, routing := range []bool{false, true} {
			o.UsageDetails, o.Routing, o.RequestTags = usage, routing, false
			base, daily, err := queries(o)
			if err != nil || strings.Contains(base, "request_tags") {
				t.Fatal("default projection changed", err)
			}
			o.RequestTags = true
			with, withDaily, err := queries(o)
			tags, _ := recipeBlock("$tags$")
			if err != nil || strings.Replace(with, tags, "", 1) != base || withDaily != daily || strings.Contains(with, "jsonb_array_elements") {
				t.Fatal("tag option changes rows or other projection bytes", err)
			}
		}
	}
}
