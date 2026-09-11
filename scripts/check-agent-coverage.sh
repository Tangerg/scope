#!/usr/bin/env bash
# Keep every Agent package inside an explicit statement-coverage budget so new
# execution surfaces cannot bypass the same regression gate as the kernel.
set -euo pipefail

cd "$(dirname "$0")/../agent"

coverage_budget=(
  ". 76.5"
  "./agenttest 76.8"
  "./strategy/collaboration 78.0"
  "./strategy/coordination 78.0"
  "./examples/autonomous 67.8"
  "./examples/composition 71.3"
  "./examples/direct_vs_managed 67.4"
  "./examples/evaluator_optimizer 72.6"
  "./examples/orchestrator_workers 67.3"
  "./examples/workflow 67.6"
  "./examples/workflow_patterns 70.8"
  "./strategy/interaction 75.5"
  "./strategy/internal/childcall 91.0"
  "./internal/conformancetest 79.1"
  "./messaging 86.5"
  "./strategy/planning 76.1"
  "./strategy/planning/goap 86.1"
  "./strategy/workflow 78.4"
)

configured_packages=$(
  for budget in "${coverage_budget[@]}"; do
    read -r package _ <<<"$budget"
    printf '%s\n' "$package"
  done | sort
)
tracked_packages=$(go list ./... | sed 's#^github.com/Tangerg/scope/agent#.#' | sort)
if [[ "$configured_packages" != "$tracked_packages" ]]; then
  echo "Agent coverage budget package inventory is stale" >&2
  diff -u <(printf '%s\n' "$configured_packages") <(printf '%s\n' "$tracked_packages") >&2 || true
  exit 1
fi

coverage_profile="${TMPDIR:-/tmp}/scope-agent-coverage-$$.out"
trap 'unlink "$coverage_profile" 2>/dev/null || true' EXIT

failed=0
for budget in "${coverage_budget[@]}"; do
  read -r package minimum <<<"$budget"
  if [[ "$package" == "./internal/conformancetest" ]]; then
    # The shared recorder is exercised by its Strategy consumers, not by a
    # separate test of the test helper.
    if ! output=$(go test -count=1 -coverpkg="$package" -coverprofile="$coverage_profile" ./strategy/interaction ./strategy/planning ./strategy/workflow 2>&1); then
      echo "$output" >&2
      exit 1
    fi
    actual=$(go tool cover -func="$coverage_profile" | awk '/^total:/ {gsub(/%/, "", $3); print $3}')
  else
    if ! output=$(go test -count=1 -cover "$package" 2>&1); then
      echo "$output" >&2
      exit 1
    fi
    actual=$(printf '%s\n' "$output" | sed -n 's/.*coverage: \([0-9][0-9.]*\)% of statements.*/\1/p')
  fi
  if [[ -z "$actual" ]]; then
    echo "could not read coverage for $package from: $output" >&2
    exit 1
  fi
  printf '%-43s %6s%% (minimum %s%%)\n' "$package" "$actual" "$minimum"
  if ! awk -v actual="$actual" -v minimum="$minimum" 'BEGIN { exit !(actual + 0 >= minimum + 0) }'; then
    failed=1
  fi
done

if [[ $failed -ne 0 ]]; then
  echo "Agent coverage budget failed" >&2
  exit 1
fi

echo "Agent coverage budget passed"
