# Two Independently Operated Systems

`data/before` and `data/after` contain synthetic canonical summaries for the
research-agent example. `owned` runs the supervisor and internal-document
specialist; `partner` runs the external-research specialist. They observe disjoint
spans within shared traces. Users and a reference document occur at both operators.

The recipe and expected answers are in [research.json](../internal/scenario/research.json).
The [live example](../two-operators/README.md) submits OTLP fixtures generated from
that same recipe to two released collectors. These files are generated directly
with sketchkit so the default demo needs no Docker, credentials, or running agents.

From the repository root:

```sh
go run ./cmd/fleetdiff compare --before examples/two-systems/data/before \
  --after examples/two-systems/data/after --expected owned,partner
```

To regenerate into a new directory without overwriting the checked-in files:

```sh
go run ./examples/two-systems -out ./fresh-fixtures
```

Add `-otlp-out ./fresh-otlp` to regenerate the live example's span fixtures too.
CI checks both sets byte for byte. The separate comparison unit-test recipe in
`internal/compare/testdata/windows.json` is not the source for this demo.

The generator is a development tool which intentionally writes only the new output
directory. It uses sketchkit's hashing API with a public fixture key, not a production secret.
Real producers must canonicalize and keyed-hash through sketchkit with operator-
managed secrets. Synthetic metadata deliberately differs from the live collector's
accounting declaration and per-run key; do not mix these two sets of exports.
The comparison command itself never writes input files.
