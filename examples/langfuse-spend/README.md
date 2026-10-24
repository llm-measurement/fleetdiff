# Synthetic Langfuse spend example

This newly authored Apache-2.0 fixture contains ten synthetic GENERATION
observations, two completion manifests and no observation payload fields.
It is not a captured production or native-exporter run.

Follow the exact command in [the Langfuse guide](../../docs/LANGFUSE.md#try-the-synthetic-example).

| Recorded value | October 7, 00:00-01:00 UTC | October 8, 00:00-01:00 UTC |
| --- | ---: | ---: |
| Requests | 4 | 6 |
| Tokens | 8,000 | 16,000 |
| Cost (USD) | 0.008 | 0.016 |
| Interactive trace tokens | 4,000 | 12,000 |
| Background trace tokens | 4,000 | 4,000 |

The interactive trace is the sole positive net contributor. Names are synthetic,
but the report still suppresses them with its normal keyed aliases. Tests run
the real importer against these exact files and check the change and coverage.
