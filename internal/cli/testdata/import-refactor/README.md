# Import Helper Refactor Goldens

These text and JSON outputs were recorded from unchanged fleetdiff v0.8.0
(`44967bd743a6fa26588742f67c54df6e323e90e0`) before extracting the shared helpers.
They cover inspect, two-period spend, cache comparison and session-budget review.
The synthetic inputs already live in `examples/`; hashes use a fixed public test
key. `internal/spend/testdata/legacy-identity.json` also pins legacy hash bytes,
including Unicode and empty-identity cases.

The tests compare complete output bytes, without deleting or normalizing fields.
The same tests must pass before and after an extraction. To deliberately update
these records, use `FLEETDIFF_UPDATE_REFACTOR_GOLDENS=1` with the two named tests,
review the resulting bytes and document the baseline change here. Ordinary CI
runs without that variable.
