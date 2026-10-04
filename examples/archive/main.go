// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

// This opt-in example writes archive files; the fleetdiff CLI remains read-only.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/compare"
)

const usage = "Usage: sh examples/archive.sh SOURCE NEW_OR_PRIVATE_ARCHIVE [--keep-windows 64]\nRetains 1 to 384 closed windows. Success is silent. Build the local helper first; see docs/SUMMARY_ARCHIVE.md."

func main() {
	os.Exit(command(os.Args[1:], os.Stdout, os.Stderr, time.Now().UnixNano()))
}

func command(args []string, out, diagnostics io.Writer, now int64) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(out, usage)
		return 0
	}
	flags := flag.NewFlagSet("archive", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	keep := flags.Int("keep-windows", 64, "closed windows to retain")
	if len(args) < 2 || flags.Parse(args[2:]) != nil || flags.NArg() != 0 || *keep < 1 || *keep > 384 {
		fmt.Fprintln(diagnostics, "archive: invalid arguments; use --help")
		return 2
	}
	if err := archive(args[0], args[1], *keep, now); err != nil {
		fmt.Fprintln(diagnostics, "archive:", err)
		return 1
	}
	return 0
}

func archive(source, destination string, keep int, now int64) error {
	if keep < 1 || keep > 384 || now < 0 {
		return errors.New("invalid retention or reference time")
	}
	source, destination, err := checkPaths(source, destination)
	if err != nil {
		return err
	}
	documents, err := compare.ReadSeries(source)
	if err != nil {
		return errors.New("source is unreadable, unsafe, invalid, or over budget")
	}
	// Validate even excluded/open snapshots before creating a new archive.
	if err := validateSeries(documents); err != nil {
		return err
	}
	root, err := openArchive(destination)
	if err != nil {
		return err
	}
	defer root.Close()
	lock, err := lockArchive(root)
	if err != nil {
		return err
	}
	defer lock.Close()
	current, err := readArchive(root)
	if err != nil {
		return err
	}
	plan, err := planArchive(current, documents, keep, now)
	if err != nil {
		return err
	}
	return installPlan(root, lock, current, plan, stageFile)
}
