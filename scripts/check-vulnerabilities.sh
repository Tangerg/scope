#!/usr/bin/env bash
# Run govulncheck as a blocking gate: every reachable finding fails.
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

packages=()
package_list=$("$root/scripts/module-packages.sh" "$module")
while IFS= read -r package; do
  [[ -z "$package" ]] || packages+=("$package")
done <<< "$package_list"
if [[ ${#packages[@]} -eq 0 ]]; then
  echo "$module: no Go packages found" >&2
  exit 2
fi

report=$(mktemp)
database_dir=${VULN_DB_DIR:-}
owned_database_dir=
if [[ -z "$database_dir" ]]; then
  database_dir=$(mktemp -d)
  owned_database_dir=1
fi
trap 'rm -f "$report"; [[ -z "$owned_database_dir" ]] || rm -rf "$database_dir"' EXIT

# Reading every module against one downloaded snapshot replaces a network fetch
# per govulncheck run, where a single dropped connection failed a whole pass.
database=$database_dir/db
if [[ ! -f "$database/index/db.json" ]]; then
  for command in curl unzip; do
    command -v "$command" >/dev/null 2>&1 || { echo "$command is required" >&2; exit 2; }
  done
  archive=$database_dir/vulndb.zip
  curl --fail --silent --show-error --location --retry 5 --retry-all-errors --max-time 300 \
    --output "$archive" https://vuln.go.dev/vulndb.zip
  rm -rf "$database"
  unzip -q "$archive" -d "$database"
  rm -f "$archive"
  [[ -f "$database/index/db.json" ]] || { echo "vulnerability database snapshot is incomplete" >&2; exit 1; }
fi

(cd "$root/$module" && govulncheck -db "file://$database" -json "${packages[@]}") >"$report"

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

if [[ ${#reachable[@]} -ne 0 ]]; then
  printf '%s: reachable vulnerabilities:\n' "$module" >&2
  printf '  %s\n' "${reachable[@]}" >&2
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
imported=()
while IFS= read -r id; do
  [[ -z "$id" ]] || imported+=("$id")
done <<< "$imported_ids"

echo "$module: no reachable vulnerabilities"
if [[ ${#imported[@]} -ne 0 ]]; then
  printf '%s: informational non-reachable module findings: %s\n' \
    "$module" "${imported[*]}"
fi
