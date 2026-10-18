// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/cli"
	"github.com/llm-measurement/fleetdiff/internal/exportdata"
)

func TestSignalsOfflineAndDatabase(t *testing.T) {
	for _, phase := range []string{"offline", "after-export", "cancel-export", "stuck-export"} {
		for _, signal := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
			t.Run(phase+"/"+signal.String(), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSignalProcessHelper$")
				cmd.Env = append(os.Environ(), "FLEETDIFF_SIGNAL_TEST="+phase)
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				stdout, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				stdin, err := cmd.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				defer stdin.Close()
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				defer cmd.Process.Kill()
				r := bufio.NewReader(stdout)
				ready, err := r.ReadString('\n')
				if err != nil || !strings.HasPrefix(ready, "ready") {
					t.Fatalf("helper not ready: %q %v", ready, err)
				}
				if err := cmd.Process.Signal(signal); err != nil {
					t.Fatal(err)
				}
				if phase == "stuck-export" {
					line, err := r.ReadString('\n')
					if err != nil || line != "canceled\n" {
						t.Fatalf("signal not delivered to exporter: %q %v", line, err)
					}
					if err := cmd.Process.Signal(signal); err != nil {
						t.Fatal(err)
					}
				}
				err = cmd.Wait()
				if err == nil || ctx.Err() != nil {
					t.Fatalf("signal did not terminate helper: %v timeout=%v stderr=%s", err, ctx.Err(), &stderr)
				}
				if phase == "cancel-export" || phase == "stuck-export" {
					directory := strings.TrimSpace(strings.TrimPrefix(ready, "ready "))
					if phase == "cancel-export" {
						if _, err := os.Stat(filepath.Dir(directory)); !os.IsNotExist(err) {
							t.Fatalf("handled export cancellation left temporary data: %v", err)
						}
						if strings.Contains(stderr.String(), directory) {
							t.Fatal("cancellation exposed temporary path")
						}
					} else {
						_ = os.RemoveAll(filepath.Dir(directory))
					}
				}
			})
		}
	}
}

type signalReadyReader struct{ io.Reader }

func (r signalReadyReader) Read(p []byte) (int, error) {
	fmt.Println("ready")
	return r.Reader.Read(p)
}

func TestSignalProcessHelper(t *testing.T) {
	phase := os.Getenv("FLEETDIFF_SIGNAL_TEST")
	if phase == "" {
		return
	}
	if phase == "offline" || phase == "after-export" {
		if phase == "after-export" {
			_, _ = withExportSignals(func(context.Context, exportdata.Options) (exportdata.Result, error) {
				return exportdata.Result{}, nil
			})(context.Background(), exportdata.Options{})
		}
		os.Args = []string{"fleetdiff", "inspect", "-"}
		os.Exit(runWithInput(signalReadyReader{os.Stdin}))
	}
	exporter := withExportSignals(func(ctx context.Context, opts exportdata.Options) (exportdata.Result, error) {
		if err := os.Mkdir(opts.Directory, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(opts.Directory, "spend.csv"), []byte("private test data"), 0600); err != nil {
			t.Fatal(err)
		}
		fmt.Println("ready " + opts.Directory)
		<-ctx.Done()
		if phase == "stuck-export" {
			fmt.Println("canceled")
			select {}
		}
		return exportdata.Result{}, exportdata.ErrInterrupted
	})
	os.Exit(cli.RunWithExporter(context.Background(), []string{"investigate", "--litellm-db"}, os.Stdin, os.Stdout, os.Stderr, exporter))
}
