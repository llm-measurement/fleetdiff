// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

// Package litellmsql embeds the public psql recipe, the sole projection source.
package litellmsql

import _ "embed"

//go:embed export.sql
var Recipe string

//go:embed daily-projection.sql
var DailyRecipe string
