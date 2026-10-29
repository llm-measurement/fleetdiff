#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
set -eu
umask 077
archive=${1:?supply a Linux release archive}
platform=${2:?supply linux/amd64 or linux/arm64}
case "$platform" in linux/amd64|linux/arm64) ;; *) exit 2 ;; esac
work=$(mktemp -d)
tag="fleetdiff-restricted-test-$$"
container="fleetdiff-restricted-test-$$"
cleanup() {
  docker rm -f "$container" >/dev/null 2>&1 || true
  docker image rm "$tag" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 130' HUP INT TERM
tar -xzf "$archive" -C "$work" fleetdiff
chmod 755 "$work/fleetdiff"
CGO_ENABLED=0 GOOS=linux GOARCH=${platform#linux/} go build -o "$work/network-probe" ./scripts/testdata/network-probe
go run ./examples/scan --out "$work/windows"
# Only synthetic fixtures and a test-only executable are exposed to the container.
chmod 755 "$work/network-probe" "$work/windows"
chmod 644 "$work/windows/"*.json
cp scripts/restricted.Dockerfile "$work/Dockerfile"
# FROM scratch has no external base to download or mutable tag to resolve.
# Pin both runtime modes to exactly the same locally built image ID.
docker build --network none --platform "$platform" --iidfile "$work/image-id" -t "$tag" "$work" >/dev/null
image=$(cat "$work/image-id")

run_container() {
  network=$1
  entrypoint=$2
  shift 2
  docker run --rm --name "$container" --platform "$platform" --network "$network" \
    --read-only --user 65532:65532 --cap-drop ALL --security-opt no-new-privileges \
    --memory 256m --cpus 1 --pids-limit 64 \
    --env FLEETDIFF_SMOKE_SECRET=synthetic-offline-smoke-only-not-a-production-secret \
    --mount "type=bind,src=$(pwd)/examples,dst=/examples,readonly" \
    --mount "type=bind,src=$(pwd)/testdata,dst=/testdata,readonly" \
    --mount "type=bind,src=$work/windows,dst=/windows,readonly" \
    --mount "type=bind,src=$work/network-probe,dst=/network-probe,readonly" \
    --entrypoint "$entrypoint" "$image" "$@"
}

# A UDP route probe sends no packets and needs no external server. It must
# succeed with bridge routing and fail with ENETUNREACH under none.
run_container bridge /network-probe bridge
run_container none /network-probe none

check_case() {
  name=$1
  expected=$2
  shift 2
  for mode in bridge none; do
    status=0
    run_container "$mode" /fleetdiff "$@" > "$work/$name.$mode.stdout" 2> "$work/$name.$mode.stderr" || status=$?
    if [ "$status" -ne "$expected" ]; then
      printf '%s (%s): expected exit %s, got %s\n' "$name" "$mode" "$expected" "$status" >&2
      cat "$work/$name.$mode.stdout" "$work/$name.$mode.stderr" >&2
      return 1
    fi
    test -s "$work/$name.$mode.stdout"
  done
  cmp "$work/$name.bridge.stdout" "$work/$name.none.stdout"
  cmp "$work/$name.bridge.stderr" "$work/$name.none.stderr"
  test ! -s "$work/$name.none.stderr"
  printf 'PASS offline equivalence: %s; exit=%s; stdout/stderr identical (%s)\n' "$name" "$expected" "$platform"
}

check_report() {
  if ! jq -e "$2" "$work/$1.none.stdout" >/dev/null; then
    printf '%s: unexpected report values\n' "$1" >&2
    cat "$work/$1.none.stdout" >&2
    return 1
  fi
}

check_case compare 0 compare --before /examples/two-systems/data/before \
  --after /examples/two-systems/data/after --expected owned,partner --format json
check_report compare '.version == 1 and .complete_observation_intervals == true and
    ([.counters[] | select(.name == "requests") | .after] == [10])'

check_case investigate-summaries 0 investigate --before /examples/sessions/data/before \
  --after /examples/sessions/data/after --expected app --format json
check_report investigate-summaries '.version == 1 and ([.questions[] | select(.id == "volume") |
    .volume.before_reported_tokens == 400 and .volume.after_reported_tokens == 3300] == [true])'

# Fixed test-only keys and explicit periods remove per-run randomness and clocks.
check_case investigate-spend 0 investigate --litellm-spend /examples/litellm-spend/synthetic.csv \
  --before-period 2026-10-07 --after-period 2026-10-08 --group-by key \
  --hash-secret-env FLEETDIFF_SMOKE_SECRET --format json
check_report investigate-spend '.before.recorded_tokens == 8000 and .after.recorded_tokens == 16000 and
    .concentration_of_increase.tracked_contributors == 2 and
    .concentration_of_increase.share_of_net_recorded_increase.lower == 1'

check_case row-coverage 0 investigate --litellm-spend /examples/litellm-spend/coverage_runtime_spend.csv \
  --litellm-daily-spend /examples/litellm-spend/coverage_runtime_daily.csv \
  --before-period 2026-10-07 --after-period 2026-10-08 \
  --hash-secret-env FLEETDIFF_SMOKE_SECRET --format json
check_report row-coverage '.row_coverage.days[0].exported_rows == 8 and
    .row_coverage.days[0].api_requests == 9 and .row_coverage.days[0].missing_rows == 1 and
    .row_coverage.days[1].status == "matched" and
    .before.recorded_tokens == 118 and .after.recorded_tokens == 15'

check_case session-budget 0 investigate --litellm-spend /examples/litellm-spend/session-budget.csv \
  --period 2026-10-03 --group-by session --session-budget-usd 1.00 \
  --hash-secret-env FLEETDIFF_SMOKE_SECRET --format json
check_report session-budget '.grouping == "session_id_only" and .sessions_with_model_calls == 4 and
    .complete_sessions_exceeding_cap == 3 and .sessions_under_multiple_identities == 1'

check_case inspect 0 inspect --format json --secret-env FLEETDIFF_SMOKE_SECRET \
  /testdata/inspect-contract/v1/litellm-normal-no-usage-provenance.json
check_report inspect '.schema == "fleetdiff-inspect/v1" and .metrics.gen_ai_sketch_requests_total == 1 and
    .metrics.gen_ai_sketch_missing_token_usage_total == 1'

check_case diagnose 0 diagnose --format json /examples/diagnose/safe.yaml
check_report diagnose '.status == "supported_safe" and .configuration_parsed == true'

check_case scan 3 scan /windows --expected app --recent 2 --as-of 2026-10-04T00:30:00Z --format json
check_report scan '.schema == "fleetdiff-scan/v1" and .unusual_windows == 2'
check_case what-if 0 what-if --litellm-spend /examples/what-if/spend.csv \
  --period 2026-10-07 --scenario /examples/what-if/scenario.json --format json
check_report what-if '.schema == "fleetdiff-what-if/v1" and .covered_observations == 3 and
    .excluded_observations.missing_split == 1 and (.level2 | length) == 3'
contract=$(cat examples/what-if-summaries/snapshots/contract.txt)
check_case what-if-summaries 0 what-if --summaries /examples/what-if-summaries/snapshots \
  --expected demo-a,demo-b --period 2026-10-07T00:00:00Z/2026-10-07T00:01:00Z \
  --model-usage-contract "$contract" --from claude-sonnet-5-5 --to gpt-6.1-sol --fraction 0.5 --format json
check_report what-if-summaries '.covered_observations == 5 and .source.observations == 9 and
    .excluded_observations.invalid_usage == 1 and .excluded_observations.missing_split == 1 and
    .excluded_observations.zero_origin_unknown == 1 and .excluded_observations.unpriced_reserved_bucket == 1'
check_case routing-audit 0 investigate --litellm-spend /examples/routing/synthetic.csv \
  --before-period 2026-10-01 --after-period 2026-10-02 --routing-audit \
  --hash-secret-env FLEETDIFF_SMOKE_SECRET --format json
check_report routing-audit '.routing_audit.model_rows.before.requests == 3 and
    .routing_audit.classifier_overhead.after.raw_usage.requests == 1 and
    .routing_audit.coverage.after.agent.missing_rows == 2 and
    .routing_audit.provider_snapshot_confirmed == false and
    .routing_audit.cohorts[0].models[0].item == "claude-sonnet-5-5" and
    (.routing_audit.cohorts[0].route | has("label") | not)'

check_case routing-labels 0 investigate --litellm-spend /examples/routing/synthetic.csv \
  --before-period 2026-10-01 --after-period 2026-10-02 --routing-audit \
  --routing-by route-agent --show-labels --acknowledge-sensitive-labels \
  --hash-secret-env FLEETDIFF_SMOKE_SECRET --format json
check_report routing-labels '.routing_audit.configuration_labels_included == true and
    .routing_audit.cohorts[0].route.label == "coding-default" and
    .routing_audit.cohorts[0].agent.label == "agent-a" and
    .routing_audit.cohorts[0].models[0].item == "claude-sonnet-5-5"'

check_case routing-scope 0 investigate --litellm-spend /examples/routing/synthetic.csv \
  --before-period 2026-10-01 --after-period 2026-10-02 --routing-audit \
  --scope-route coding-default --hash-secret-env FLEETDIFF_SMOKE_SECRET --format json
check_report routing-scope '.scope_coverage.after.matched_rows == 3 and
    .scope_coverage.after.nonmatching_known_rows == 1 and
    .after.logged_model_requests == 2 and (.routing_audit.cohorts | length) == 1'

check_case what-if-routing 0 what-if --litellm-spend /examples/routing/synthetic.csv \
  --period 2026-10-02 --from claude-sonnet-5-5 --to gpt-6.1-sol --fraction 0.5 \
  --scope-route coding-default --format json
check_report what-if-routing '.covered_observations == 2 and
    .excluded_observations.missing_split == 1 and
    .excluded_observations.unsupported_call_type == 1 and .moves[0].scoped == true'
check_case agent-id 0 investigate --litellm-spend /examples/litellm-spend/agents.csv \
  --before-period 2026-10-07 --after-period 2026-10-08 --group-by agent \
  --agent-source agent_id --rank-by recorded-spend-change \
  --hash-secret-env FLEETDIFF_SMOKE_SECRET --format json
check_report agent-id '.agents.label_source == "agent_id" and
    .agents.contributors[0].recorded_usd_delta == "2" and
    .agents.contributors[0].after.recorded_usd == "3" and
    (.agents.contributors[0] | has("label") | not)'

check_case agent-tags 0 investigate --litellm-spend /examples/litellm-spend/agent-tags.csv \
  --before-period 2026-10-07 --after-period 2026-10-08 --group-by agent \
  --agent-source request-tag --agent-tag-prefix 'agent:' --rank-by recorded-spend-change \
  --hash-secret-env FLEETDIFF_SMOKE_SECRET --format json
check_report agent-tags '.after.recorded_tokens == 620 and .after.recorded_spend_usd == "6.2" and
    .agents.after.known.recorded_tokens == 500 and
    .agents.after.unknown.recorded_tokens == 50 and
    .agents.after.ambiguous.recorded_tokens == 70 and
    .agents.after.coverage.repeated_agent_tag_rows == 1 and
    .agents.after.coverage.empty_suffix_rows == 1'

check_case agent-labels 0 investigate --litellm-spend /examples/litellm-spend/agent-tags.csv \
  --before-period 2026-10-07 --after-period 2026-10-08 --group-by agent \
  --agent-source request-tag --agent-tag-prefix 'agent:' --rank-by recorded-spend-change \
  --show-labels --acknowledge-sensitive-labels \
  --hash-secret-env FLEETDIFF_SMOKE_SECRET --format json
check_report agent-labels '.agents.sensitive_labels_included == true and
    .agents.contributors[0].label == "alpha"'
printf 'PASS restricted offline binary smoke: 17 cases (%s)\n' "$platform"
