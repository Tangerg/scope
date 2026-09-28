#!/usr/bin/env bash
# Run govulncheck as a blocking gate. Only the reviewed, currently unfixable
# Ollama findings are allowed in models/ollama; every other reachable
# finding fails. If Ollama's version changes or the database publishes a fixed
# version, this script fails so the exception must be removed or re-reviewed.
set -eo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
module=${1:-}

if [[ -z "$module" || ! -f "$root/$module/go.mod" ]]; then
  echo "usage: $0 <workspace-module>" >&2
  exit 2
fi
if ! command -v govulncheck >/dev/null 2>&1; then
  echo "govulncheck is required" >&2
  exit 2
fi
if ! command -v jq >/dev/null 2>&1; then
  echo "jq is required" >&2
  exit 2
fi

report=$(mktemp)
trap 'rm -f "$report"' EXIT
packages=()
package_list=$("$root/scripts/module-packages.sh" "$module")
while IFS= read -r package; do
  [[ -z "$package" ]] || packages+=("$package")
done <<< "$package_list"
if [[ ${#packages[@]} -eq 0 ]]; then
  echo "$module: no Go packages found" >&2
  exit 2
fi
(cd "$root/$module" && govulncheck -json "${packages[@]}") >"$report"

reachable_ids=$(
  jq -rs '
    [.[] | .finding?
      | select(. != null)
      | select(any(.trace[]?; has("function")))
      | .osv]
    | unique[]
  ' "$report"
)
reachable=()
while IFS= read -r id; do
  [[ -z "$id" ]] || reachable+=("$id")
done <<< "$reachable_ids"

allowed=()
case "$module" in
  models/ollama)
    allowed=(
      GO-2025-3557
      GO-2025-3558
      GO-2025-3559
      GO-2025-3582
      GO-2025-3689
      GO-2025-3695
      GO-2025-3824
      GO-2025-4251
    )
    ;;
esac

unexpected=()
for id in "${reachable[@]}"; do
  permitted=0
  for accepted in "${allowed[@]}"; do
    if [[ "$id" == "$accepted" ]]; then
      permitted=1
      break
    fi
  done
  if [[ $permitted -eq 0 ]]; then
    unexpected+=("$id")
    continue
  fi

  if ! jq -ers --arg id "$id" '
    any(.[].finding?;
      .osv == $id and
      any(.trace[]?;
        .module == "github.com/ollama/ollama" and .version == "v0.32.5"))
  ' "$report" >/dev/null; then
    echo "$module: $id no longer matches reviewed Ollama v0.32.5 risk" >&2
    exit 1
  fi
  if ! jq -ers --arg id "$id" '
    any(.[].osv?;
      .id == $id and
      ([.affected[]?.ranges[]?.events[]? | select(has("fixed"))] | length == 0))
  ' "$report" >/dev/null; then
    echo "$module: $id now has a fixed version; upgrade instead of allowing it" >&2
    exit 1
  fi
done

if [[ ${#unexpected[@]} -ne 0 ]]; then
  printf '%s: unexpected reachable vulnerabilities:\n' "$module" >&2
  printf '  %s\n' "${unexpected[@]}" >&2
  exit 1
fi

imported_ids=$(
  jq -rs '
    [.[] | .finding?
      | select(. != null)
      | select(any(.trace[]?; has("function")) | not)
      | .osv]
    | unique[]
  ' "$report"
)
imported_candidates=()
while IFS= read -r id; do
  [[ -z "$id" ]] || imported_candidates+=("$id")
done <<< "$imported_ids"

imported=()
for id in "${imported_candidates[@]}"; do
  symbol_reachable=0
  for reachable_id in "${reachable[@]}"; do
    if [[ "$id" == "$reachable_id" ]]; then
      symbol_reachable=1
      break
    fi
  done
  if [[ $symbol_reachable -eq 0 ]]; then
    imported+=("$id")
  fi
done

if [[ ${#reachable[@]} -eq 0 ]]; then
  echo "$module: no reachable vulnerabilities"
else
  printf '%s: only reviewed unfixable Ollama findings remain: %s\n' \
    "$module" "${reachable[*]}"
fi
if [[ ${#imported[@]} -ne 0 ]]; then
  printf '%s: informational non-reachable module findings: %s\n' \
    "$module" "${imported[*]}"
fi
