# A Two-Operator Trial

Start with one question: **what changed in reported usage across two systems, and
is our coverage good enough to interpret it?** Keep the existing trace backends.
No fleetdiff account, shared dashboard, or raw-trace upload is needed.

## First Answer

Run the [collector-to-fleetdiff example](../examples/two-operators/README.md).
Answer the five questions in the walkthrough: did reported usage fall, did model
activity rise with flat runs, did more resources appear, did error signatures
increase, and is an operator missing? Compare the owned system's -440 token change
with the combined +600 change. Find 6 to 10 model requests, 2 root-agent runs in
both windows, approximately 1 to 4 resources, and the tool-error bounds. Two requests
lack usage in each window. Then remove the partner's export and explain why a partial lower
total is not evidence of savings. None of these measurements establish answer quality.

## Handoff Between People

1. Agree on non-sensitive producer IDs, the expected producer inventory, scope,
   window duration, extraction/accounting settings, and permitted key linkage.
   Identify which requests each operator owns so observations do not overlap.
2. Each operator configures the collector's existing
   [summary export](https://github.com/llm-measurement/otelcol-genai-sketches/blob/v0.1.0/docs/SUMMARY_EXCHANGE.md).
   Choose two completed windows, not the startup window or a still-running window.
   Keep the data and configuration within approved environments.
3. Send just those completed summary files through an approved authenticated
   channel. Preserve their contents. Keep keys, raw traces, and diagnostic logs
   at the producing system. Check that the receiving person may see the metadata,
   pseudonyms, and aggregates; absence of raw prompts is not permission to share.
4. The recipient places the received snapshots into `before` and `after`
   directories and runs `fleetdiff compare --before before --after after
   --expected owned,partner`. Use the actual agreed producer IDs. Keep the same
   expected inventory on both sides; dropping a missing producer hides a gap.
5. Review missing producers, partial intervals, and token coverage before the
   changes. Incompatible settings need investigation, not relabeling. A key ID is
   a declaration, not proof of key equality. Missing raw events, overlapping
   observations, or a false producer declaration cannot be discovered reliably
   from sketches alone.

The file transfer mechanism and authorization are supplied by the operators.
Checksums can detect an accidental change when delivered through a trusted channel;
they do not authenticate a supplier. The current summary format has no signature.
Keep separate customer scopes where linkage is not permitted.

## Voluntary Outcome Record

Record only what the participants agree to share. Leave names and workload details
out of public issues unless authorized. The tool sends no trial telemetry.

- [ ] Installed and ran the controlled example.
- [ ] Explained the complete and missing-operator results correctly.
- [ ] Produced exports from approved own traffic, with source versions recorded.
- [ ] A different person imported the summaries without requesting raw traces.
- [ ] Recorded time to first answer and the first point of confusion.
- [ ] Used the comparison again for a real investigation.
- [ ] Asked another team or supplier for a compatible export.

A successful example is not a production deployment. A colleague handoff is not
an external customer trial. A request for another supplier's export is useful
feedback, not an endorsement or proof of a network effect.

## Removal

Stop the local example's containers and remove its output directory after reviewing
what should be retained. For a real deployment, disable `summary_export`, restart
the collector using its normal process, and apply the agreed retention policy to
exports and reports. Removing fleetdiff does not remove the existing trace backend.
