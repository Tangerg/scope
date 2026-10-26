#!/usr/bin/env bash
# One module profile includes consumer tests and internal owners; package
# ratchets use the same evidence, never a second coverage denominator.
set -euo pipefail

cd "$(dirname "$0")/.."
module_budget=(
  "a2a 90"
  "agent 90"
  "core 90"
  "dev/providerconformance 90"
  "dev/repoarch none"
  "dev/treecommitterconformance none"
  "etl 90"
  "etl/html 90"
  "etl/pdf 90"
  "eval 90"
  "eval/trajectory 90"
  "examples 72"
  "historystores/cassandra 39.8"
  "historystores/cosmosdb 22.0"
  "historystores/mongodb 29.3"
  "historystores/neo4j 33.1"
  "historystores/postgres 45.9"
  "historystores/redis 32.0"
  "mcp 90"
  "models/alibaba 79.2"
  "models/anthropic 42.9"
  "models/assemblyai 66.9"
  "models/azureopenai 61.8"
  "models/bedrock 41.7"
  "models/blackforestlabs 64.6"
  "models/catalog 90"
  "models/cohere 72.1"
  "models/deepgram 61.4"
  "models/deepseek 73.6"
  "models/elevenlabs 49.4"
  "models/fireworks 13/14"
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
  "otel 90"
  "otel/agent 90"
  "otel/eval 90"
  "otel/rag 90"
  "rag 90"
  "skills 90"
  "tokenizers/tiktoken 90"
  "tools 90"
  "tools/skills 90"
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

# The existing Core and Agent package ratchets remain exhaustive; the managed
# consumer keeps its budget after moving into the examples module.
package_inventory_modules=(core agent)
package_budget=(
  "agent 86.8"
  "agent/agenttest 81.2"
  "agent/examples/autonomous 70.2"
  "agent/examples/composition 78.4"
  "agent/examples/evaluator_optimizer 75.7"
  "agent/examples/orchestrator_workers 70.2"
  "agent/examples/workflow 70.3"
  "agent/examples/workflow_patterns 72.2"
  "agent/internal/conformancetest 81.6"
  "agent/internal/jsonwire 100.0"
  "agent/internal/panicinfo 100.0"
  "agent/messaging 88.6"
  "agent/strategy/collaboration 85.5"
  "agent/strategy/coordination 80.1"
  "agent/strategy/interaction 84.2"
  "agent/strategy/internal/childcall 95.3"
  "agent/strategy/internal/restore 100.0"
  "agent/strategy/internal/stepfail 100.0"
  "agent/strategy/planning 82.4"
  "agent/strategy/planning/goap 87.8"
  "agent/strategy/role 87.5"
  "agent/strategy/workflow 79.0"
  "core none"
  "core/chat 89.2"
  "core/chatclient 88.8"
  "core/chatclient/internal/arch none"
  "core/chatclient/safeguard 93.1"
  "core/document 91.3"
  "core/embedding 96.8"
  "core/embeddingclient 92.9"
  "core/embeddingclient/internal/arch none"
  "core/history 90.8"
  "core/history/inmemory 100.0"
  "core/history/storetest 86.2"
  "core/httpclient 96.4"
  "core/image 97.3"
  "core/internal/arch none"
  "core/internal/modelid 100"
  "core/internal/ptr 100.0"
  "core/jsonschema 89.4"
  "core/media 92.9"
  "core/metadata 86.2"
  "core/modeltest 79.2"
  "core/moderation 96.9"
  "core/rerank 92.8"
  "core/speech 96.9"
  "core/timestamp 100.0"
  "core/tokenizer none"
  "core/tokenizer/internal/arch none"
  "core/tool 92.6"
  "core/tool/internal/arch none"
  "core/transcription 96.1"
  "core/vectorstore 86.9"
  "core/vectorstore/filter 84.2"
  "core/vectorstore/inmemory 89.4"
  "core/vectorstore/storetest 62.8"
  "examples/agent/direct_vs_managed 69.1"
)

configured_modules=$(printf '%s\n' "${module_budget[@]}" | awk '{print $1}' | sort)
tracked_modules=$(scripts/workspace-modules.sh | sort)
if [[ "$configured_modules" != "$tracked_modules" ]]; then
  echo "coverage budget module inventory is stale" >&2
  diff -u <(printf '%s\n' "$configured_modules") <(printf '%s\n' "$tracked_modules") >&2 || true
  exit 1
fi

# A ratio preserves an existing exact baseline when its displayed percentage
# rounded up; coverage must not depend on display precision.
budget_ratio() {
  local minimum=$1
  if [[ "$minimum" =~ ^([0-9]+)/([1-9][0-9]*)$ ]]; then
    printf '%s %s\n' "${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}"
  elif [[ "$minimum" =~ ^[0-9]+([.][0-9]+)?$ ]]; then
    printf '%s 100\n' "$minimum"
  else
    echo "invalid coverage budget: $minimum" >&2
    return 1
  fi
}

minimum_provider_coverage=20
for budget in "${module_budget[@]}"; do
  read -r module minimum <<< "$budget"
  case "$module" in
    historystores/*|models/*|vectorstores/*)
      ratio=$(budget_ratio "$minimum")
      read -r numerator denominator <<< "$ratio"
      if ! awk -v numerator="$numerator" -v denominator="$denominator" -v floor="$minimum_provider_coverage" 'BEGIN {exit numerator*100 < floor*denominator}'; then
        echo "$module coverage budget is below the provider floor $minimum_provider_coverage%" >&2
        exit 1
      fi
      ;;
  esac
done

target=${1:-}
if [[ -n "$target" ]] && ! awk -v target="$target" '$0 == target {found=1} END {exit !found}' <<< "$configured_modules"; then
  echo "unknown coverage module: $target" >&2
  exit 2
fi

check_package_inventory() {
  local module=$1 configured tracked
  configured=$(
    for budget in "${package_budget[@]}"; do
      read -r package _ <<< "$budget"
      if [[ "$package" == "$module" || "$package" == "$module/"* ]]; then
        printf '%s\n' "github.com/Tangerg/scope/$package"
      fi
    done | sort
  )
  tracked=$(cd "$module" && go list ./... | sort)
  if [[ "$configured" != "$tracked" ]]; then
    echo "$module coverage package inventory is stale" >&2
    diff -u <(printf '%s\n' "$configured") <(printf '%s\n' "$tracked") >&2 || true
    return 1
  fi
}

for module in "${package_inventory_modules[@]}"; do
  if [[ -z "$target" || "$target" == "$module" ]]; then
    check_package_inventory "$module"
  fi
done

profile=$(mktemp "${TMPDIR:-/tmp}/scope-coverage.XXXXXXXX")
trap 'unlink "$profile"' EXIT

# Profiles can repeat a block across package test binaries. A statement belongs
# to one source block and is covered if any consumer executed it.
profile_statistics() {
  awk -v prefix="github.com/Tangerg/scope/$module/" '
    NR == 1 {
      if ($0 !~ /^mode: (set|count|atomic)$/) {invalid=1; exit 1}
      next
    }
    NF != 3 || index($1, prefix) != 1 || $1 !~ /:[0-9]+\.[0-9]+,[0-9]+\.[0-9]+$/ || $2 !~ /^[0-9]+$/ || $3 !~ /^[0-9]+$/ {
      invalid=1; exit 1
    }
    {
      if ($1 in statements && statements[$1] != $2) {invalid=1; exit 1}
      statements[$1]=$2
      if ($3 > 0) covered[$1]=1
    }
    END {
      if (invalid || NR == 0) exit 1
      for (block in statements) {
        package=block
        sub(/\/[^\/]+$/, "", package)
        sub(/^github.com\/Tangerg\/scope\//, "", package)
        total[package]+=statements[block]
        hit[package]+=statements[block]*covered[block]
        all+=statements[block]
        executed+=statements[block]*covered[block]
      }
      printf "module %d %d\n", executed, all
      for (package in total) printf "%s %d %d\n", package, hit[package], total[package]
    }
  ' "$profile"
}

check_budget() {
  local subject=$1 key=$2 minimum=$3 counts ratio numerator=0 denominator=1 label=$3
  counts=$(awk -v key="$key" '$1 == key {print $2, $3; found=1} END {if (!found) print 0, 0}' <<< "$statistics")
  if [[ "$minimum" != "none" ]]; then
    ratio=$(budget_ratio "$minimum") || return 1
    read -r numerator denominator <<< "$ratio"
    [[ "$minimum" == */* ]] || label="$minimum%"
  fi
  awk -v subject="$subject" -v minimum="$minimum" -v label="$label" -v numerator="$numerator" -v denominator="$denominator" '
    {
      if (minimum == "none") {
        printf "%-55s %s\n", subject, ($2 == 0 ? "no statements" : "unexpected executable statements")
        exit $2 != 0
      }
      if ($2 == 0) {
        printf "%s: no executable statements for numeric coverage budget\n", subject > "/dev/stderr"
        exit 1
      }
      printf "%-55s %8.3f%% (%d/%d; minimum %s)\n", subject, 100*$1/$2, $1, $2, label
      exit $1*denominator < numerator*$2
    }
  ' <<< "$counts"
}

failed=0
for budget in "${module_budget[@]}"; do
  read -r module minimum <<< "$budget"
  [[ -z "$target" || "$target" == "$module" ]] || continue
  : > "$profile"
  if ! output=$(cd "$module" && go test -count=1 -coverpkg=./... -coverprofile="$profile" ./... 2>&1); then
    echo "$output" >&2
    exit 1
  fi
  if ! statistics=$(profile_statistics); then
    echo "$module: unreadable coverage profile" >&2
    exit 1
  fi
  check_budget "$module" module "$minimum" || failed=1
  for package_entry in "${package_budget[@]}"; do
    read -r package minimum <<< "$package_entry"
    if [[ "$package" == "$module" || "$package" == "$module/"* ]]; then
      check_budget "$package" "$package" "$minimum" || failed=1
    fi
  done
done

if [[ $failed -ne 0 ]]; then
  echo "coverage budget failed" >&2
  exit 1
fi
echo "coverage budget passed"
