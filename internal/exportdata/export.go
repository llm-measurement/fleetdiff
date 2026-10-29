// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

// Package exportdata defines the boundary between offline reports and an explicit export.
package exportdata

import (
	"context"
	"time"
)

type Options struct {
	Directory, CredentialFile    string
	Start, End                   time.Time
	Format                       string
	Daily, Routing, UsageDetails bool
	RequestTags                  bool
}

type Result struct {
	Directory, SpendPath, DailyPath string
	Start, End                      time.Time
	Rows                            int64
}

type Exporter func(context.Context, Options) (Result, error)

// Failure has no untrusted text fields. The CLI may render these fixed messages,
// while suppressing errors from arbitrary exporter implementations.
type Failure uint8

const (
	ErrCredentials Failure = iota + 1
	ErrConfiguration
	ErrTransport
	ErrOutput
	ErrSchema
	ErrLimit
	ErrInterrupted
	ErrDatabase
	ErrStorage
)

func (e Failure) Error() string {
	switch e {
	case ErrCredentials:
		return "provide one database URL through FLEETDIFF_LITELLM_DATABASE_URL or a regular, owned mode-0600 database-url-file"
	case ErrConfiguration:
		return "invalid database URL; specify one host, database and user, with only sslmode and optional sslrootcert (or host for a Unix socket)"
	case ErrTransport:
		return "remote databases require sslmode=verify-full; only numeric loopback or Unix sockets permit sslmode=disable"
	case ErrOutput:
		return "export needs a new output directory under an existing directory, without symlink components"
	case ErrSchema:
		return "database schema does not match the LiteLLM v1.104.0 export profile; verify the pinned schema or use the psql recipe"
	case ErrLimit:
		return "export exceeds the request, file, row or field limit; select a shorter period"
	case ErrInterrupted:
		return "database export interrupted or timed out; no complete export was published"
	case ErrStorage:
		return "cannot finish private export files; check available disk space and output access"
	default:
		return "database export failed; check read access, connection settings and the LiteLLM v1.104.0 export profile"
	}
}
