#!/usr/bin/env bash
# Enforce a ratcheted Core statement-coverage budget package by package. Every
# public package must be listed, including packages with no statements, so a
# new package cannot silently bypass the gate. Selected internal owners with
# executable behavior are covered by the same budget.
set -euo pipefail

cd "$(dirname "$0")/../core"

coverage_budget=(
  ". none"
  "./chat 89.2"
  "./chatclient 88.8"
  "./chatclient/safeguard 93.1"
  "./document 91.3"
  "./embedding 96.8"
  "./embeddingclient 92.9"
  "./history 90.8"
  "./history/inmemory 100.0"
  "./history/storetest 86.2"
  "./image 97.3"
  "./internal/ptr 100.0"
  "./internal/wiretime 100.0"
  "./jsonschema 89.4"
  "./media 92.9"
  "./metadata 86.2"
  "./modeltest 79.2"
  "./moderation 96.9"
  "./rerank 92.8"
  "./speech 96.9"
  "./tokenizer none"
  "./tool 92.6"
  "./transcription 96.1"
  "./vectorstore 86.9"
  "./vectorstore/filter 84.2"
  "./vectorstore/inmemory 89.4"
  "./vectorstore/storetest 62.8"
)

configured_packages=$(
  for budget in "${coverage_budget[@]}"; do
    read -r package _ <<<"$budget"
    printf '%s\n' "$package"
  done | sort
)
tracked_packages=$(
  {
    go list ./... |
      sed 's#^github.com/Tangerg/scope/core#.#' |
      awk '$0 !~ /\/internal(\/|$)/'
    printf '%s\n' ./internal/ptr ./internal/wiretime
  } | sort
)
if [[ "$configured_packages" != "$tracked_packages" ]]; then
  echo "Core coverage budget package inventory is stale" >&2
  diff -u <(printf '%s\n' "$configured_packages") <(printf '%s\n' "$tracked_packages") >&2 || true
  exit 1
fi

failed=0
for budget in "${coverage_budget[@]}"; do
  read -r package minimum <<<"$budget"
  if ! output=$(go test -count=1 -cover "$package" 2>&1); then
    echo "$output" >&2
    exit 1
  fi
  if [[ "$minimum" == "none" ]]; then
    if [[ "$output" != *"coverage: [no statements]"* ]]; then
      echo "$package gained executable statements; assign it a coverage budget" >&2
      echo "$output" >&2
      failed=1
    else
      printf '%-43s %s\n' "$package" "no statements"
    fi
    continue
  fi
  actual=$(printf '%s\n' "$output" | sed -n 's/.*coverage: \([0-9][0-9.]*\)% of statements.*/\1/p')
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
  echo "Core coverage budget failed" >&2
  exit 1
fi

echo "Core coverage budget passed"
