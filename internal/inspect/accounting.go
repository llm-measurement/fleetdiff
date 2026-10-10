// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package inspect

import (
	"errors"
	"github.com/llm-measurement/fleetdiff/internal/accounting"
	commonpb "go.opentelemetry.io/proto/slim/otlp/common/v1"
)

const AccountingID = accounting.ID
const metricPrefix = accounting.Prefix

var tokenFields = accounting.Fields
var accountTokens = accounting.Tokens
var tokenNumber = accounting.Number
var modelAttempt = accounting.ModelAttempt
var mergeCounts = accounting.MergeCounts
var errOverflow = errors.New("observed token value, counter, or sketch weight exceeds its supported range")

func stringAttribute(attrs map[string]*commonpb.AnyValue, key string) string {
	return attrs[key].GetStringValue()
}
