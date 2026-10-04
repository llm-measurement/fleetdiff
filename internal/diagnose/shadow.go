// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package diagnose

import _ "embed"

//go:embed shadow.yaml
var shadowConfig string

// ShadowConfig is deliberately independent of the input and process environment.
// It is a standalone proposal, never a patch to the existing collector.
func ShadowConfig() string { return shadowConfig }
