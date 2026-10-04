# Which Windows Need Attention?

From the checkout root:

```sh
sh examples/scan.sh
sh examples/scan.sh --quiet
```

This Go-only demo creates 30 one-minute summary windows in a private temporary
directory, builds the current CLI, and removes the directory afterwards. No
Docker, network listener, API key, or model calls are involved.

The first 28 windows have 100 attempts and 10,000 recorded tokens each. The
penultimate window has 31,000 tokens; one session carries 62% of the attributed
tokens and attempts. The final window has 40 attempts missing usage. Both final
windows contain 40 tool errors with the same signature.

The scanner reports two unusual windows, distinguishes the coverage change
from consumption, and hides all key hashes. The quiet variant retains the steady
baseline throughout and exits without flags. Its wrapper accepts exit 3 only
for the planted scenario; it fails if the expected result is absent.

`main_test.go` checks these values directly. To retain the synthetic files for
experimentation, supply a **new** directory:

```sh
go run ./examples/scan --out ./synthetic-windows
go run ./cmd/fleetdiff scan ./synthetic-windows --expected app --recent 2 \
  --as-of 2026-10-04T00:30:00Z
```

`go run` wraps the program's exit status; build the binary for cron or CI that
needs the exact exit code. See [scan options and interpretation](../../docs/SCAN.md).
