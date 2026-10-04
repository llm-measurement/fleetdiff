# Diagnose Fixtures

Run `sh examples/diagnose.sh` from the checkout. It builds a temporary local
binary, prints compact findings for all three fixtures, and checks standalone
shadow proposal generation.
It does not start a collector or change an existing backend. With a populated
Go module cache, `GOPROXY=off` makes the build offline too.

| Fixture | Expected status | Reason |
| --- | --- | --- |
| `safe.yaml` | 0 | All configured mappings fall within supported static checks. |
| `unsafe.yaml` | 3 | User identity is a plaintext metric label; non-model operations are counted. |
| `unsupported.yaml` | 4 | Transform, interpolation, and custom dimension cannot be certified. |

All data is synthetic. To see the proposal, run
`fleetdiff diagnose --shadow-config collector.yaml` and review the placeholders.

See [Diagnose](../../docs/DIAGNOSE.md) for the support boundary, stable finding IDs,
review requirements, collector validation, and independent producer inventory.
