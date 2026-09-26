# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
FROM scratch
COPY fleetdiff /fleetdiff
USER 65532:65532
ENTRYPOINT ["/fleetdiff"]
