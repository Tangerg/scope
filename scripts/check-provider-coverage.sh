#!/usr/bin/env bash
# Ratchet statement coverage for every independently versioned provider module.
# Passing one module path checks only that module; inventory and the repository
# floor are still checked globally so new providers cannot bypass the gate.
set -euo pipefail

cd "$(dirname "$0")/.."

minimum_provider_coverage=20.0
coverage_budget=(
  "historystores/cassandra 39.8"
  "historystores/cosmosdb 22.0"
  "historystores/mongodb 29.3"
  "historystores/neo4j 33.1"
  "historystores/postgres 45.9"
  "historystores/redis 32.0"
  "models/alibaba 28.6"
  "models/anthropic 42.9"
  "models/assemblyai 66.9"
  "models/azureopenai 61.8"
  "models/bedrock 41.7"
  "models/blackforestlabs 64.6"
  "models/catalog 89.3"
  "models/cohere 72.1"
  "models/deepgram 61.4"
  "models/deepseek 73.6"
  "models/elevenlabs 49.4"
  "models/fireworks 36.4"
  "models/gladia 62.6"
  "models/google 60.5"
  "models/groq 36.4"
  "models/huggingface 36.4"
  "models/hume 59.8"
  "models/jina 72.7"
  "models/luma 67.9"
  "models/minimax 50.0"
  "models/mistral 61.1"
  "models/moonshot 44.9"
  "models/nomic 73.1"
  "models/ollama 73.3"
  "models/openai 68.2"
  "models/openrouter 41.9"
  "models/perplexity 58.8"
  "models/prodia 63.0"
  "models/protocol/anthropic 72.0"
  "models/protocol/openai 64.0"
  "models/replicate 63.3"
  "models/revai 63.4"
  "models/stability 67.9"
  "models/together 36.4"
  "models/voyage 72.4"
  "models/xai 36.4"
  "models/xiaomi 50.0"
  "models/zhipu 81.2"
  "vectorstores/azureaisearch 40.1"
  "vectorstores/azurecosmos 31.2"
  "vectorstores/bedrockkb 55.7"
  "vectorstores/cassandra 28.1"
  "vectorstores/chroma 33.7"
  "vectorstores/clickhouse 34.8"
  "vectorstores/couchbase 34.6"
  "vectorstores/elasticsearch 35.6"
  "vectorstores/mariadb 37.3"
  "vectorstores/milvus 33.6"
  "vectorstores/mongodb 49.7"
  "vectorstores/neo4j 28.4"
  "vectorstores/opensearch 38.5"
  "vectorstores/oracle 34.0"
  "vectorstores/pinecone 67.1"
  "vectorstores/postgres 37.4"
  "vectorstores/qdrant 57.1"
  "vectorstores/redis 43.3"
  "vectorstores/s3vectors 23.4"
  "vectorstores/tidb 23.0"
  "vectorstores/typesense 33.8"
  "vectorstores/vectara 24.9"
  "vectorstores/vespa 33.2"
  "vectorstores/weaviate 41.6"
)

configured_modules=$(
  for budget in "${coverage_budget[@]}"; do
    read -r module _ <<<"$budget"
    printf '%s\n' "$module"
  done | sort
)
tracked_modules=$(scripts/workspace-modules.sh | awk '/^(historystores|models|vectorstores)\//' | sort)
if [[ "$configured_modules" != "$tracked_modules" ]]; then
  echo "Provider coverage budget inventory is stale" >&2
  diff -u <(printf '%s\n' "$configured_modules") <(printf '%s\n' "$tracked_modules") >&2 || true
  exit 1
fi

for budget in "${coverage_budget[@]}"; do
  read -r module minimum <<<"$budget"
  if ! awk -v minimum="$minimum" -v floor="$minimum_provider_coverage" 'BEGIN { exit !(minimum + 0 >= floor + 0) }'; then
    echo "$module coverage budget $minimum% is below the repository floor $minimum_provider_coverage%" >&2
    exit 1
  fi
done

target_module=${1:-}
if [[ -n "$target_module" ]] && ! printf '%s\n' "$configured_modules" | grep -Fxq "$target_module"; then
  echo "unknown provider module: $target_module" >&2
  exit 2
fi

coverage_profile="${TMPDIR:-/tmp}/scope-provider-coverage-$$.out"
trap 'unlink "$coverage_profile" 2>/dev/null || true' EXIT

failed=0
for budget in "${coverage_budget[@]}"; do
  read -r module minimum <<<"$budget"
  if [[ -n "$target_module" && "$module" != "$target_module" ]]; then
    continue
  fi
  if ! output=$(cd "$module" && go test -count=1 -coverprofile="$coverage_profile" ./... 2>&1); then
    echo "$output" >&2
    exit 1
  fi
  actual=$(go tool cover -func="$coverage_profile" | awk '/^total:/ {gsub(/%/, "", $3); print $3}')
  if [[ -z "$actual" ]]; then
    echo "could not read coverage for $module from: $output" >&2
    exit 1
  fi
  printf '%-43s %6s%% (minimum %s%%)\n' "$module" "$actual" "$minimum"
  if ! awk -v actual="$actual" -v minimum="$minimum" 'BEGIN { exit !(actual + 0 >= minimum + 0) }'; then
    failed=1
  fi
done

if [[ $failed -ne 0 ]]; then
  echo "Provider coverage budget failed" >&2
  exit 1
fi

echo "Provider coverage budget passed"
