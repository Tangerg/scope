#!/usr/bin/env bash
# Keep every Agent package inside an explicit statement-coverage budget so new
# execution surfaces cannot bypass the same regression gate as the kernel.
set -euo pipefail

cd "$(dirname "$0")/../agent"

coverage_budget=(
  ". 86.8"
  "./agenttest 81.2"
  "./strategy/collaboration 85.5"
  "./strategy/coordination 80.1"
  "./examples/autonomous 70.4"
  "./examples/composition 78.4"
  "./examples/direct_vs_managed 69.1"
  "./examples/evaluator_optimizer 75.7"
  "./examples/orchestrator_workers 70.2"
  "./examples/workflow 70.4"
  "./examples/workflow_patterns 73.3"
  "./strategy/interaction 84.2"
  "./strategy/internal/childcall 95.3"
  "./internal/conformancetest 81.6"
  "./internal/jsonwire 100.0"
  "./internal/panicinfo 100.0"
  "./messaging 88.6"
  "./strategy/planning 82.4"
  "./strategy/planning/goap 87.8"
  "./strategy/workflow 79.0"
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
    # Include direct authority-guard tests and the Strategy consumers that
    # exercise the shared Engine-boundary recorder.
    if ! output=$(go test -count=1 -coverpkg="$package" -coverprofile="$coverage_profile" "$package" ./strategy/interaction ./strategy/planning ./strategy/workflow 2>&1); then
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
