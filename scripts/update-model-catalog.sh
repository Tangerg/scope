#!/usr/bin/env bash
# Refresh the embedded model catalog from its two upstream sources, report what
# moved, and run the module's checks.
#
# Usage:
#   scripts/update-model-catalog.sh
#   scripts/update-model-catalog.sh -source ./api.json -ladders ./catwalk.json
#
# The two sources own disjoint facts: models.dev decides which models exist and
# carries every field but the reasoning effort ladder, catwalk carries the
# ladder. models/catalog/gen.go explains the split and the guards enforcing it.
#
# The generator writes nothing until every provider has mapped, so a failed run
# leaves the checked-in data untouched. This script never commits: read the
# summary, review the diff, then commit the generator inputs and the regenerated
# data as separate changes.
#
# Required tools: go, jq.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
catalog_dir=$root/models/catalog

fail() {
  printf 'update-model-catalog: %s\n' "$*" >&2
  exit 1
}

command -v jq >/dev/null 2>&1 || fail 'jq is required'
[[ -f "$catalog_dir/gen.go" ]] || fail "no generator at $catalog_dir/gen.go"

# Snapshot paths are resolved against the caller's directory, not the module the
# generator has to run in.
generator_args=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    -source | -ladders)
      [[ $# -ge 2 ]] || fail "$1 needs a value"
      value=$2
      case "$value" in
        http://* | https://* | /*) ;;
        *) value=$PWD/$value ;;
      esac
      generator_args+=("$1" "$value")
      shift 2
      ;;
    *) fail "unknown argument: $1 (accepts -source and -ladders)" ;;
  esac
done

previous_dir=$(mktemp -d /tmp/scope-catalog.XXXXXX)
cleanup() {
  [[ "$previous_dir" == /tmp/scope-catalog.* ]] && rm -rf "$previous_dir"
}
trap cleanup EXIT
cp "$catalog_dir"/configs/*.json "$previous_dir/"

printf '── regenerating models/catalog\n'
(cd "$catalog_dir" && go run gen.go ${generator_args[@]+"${generator_args[@]}"}) >/dev/null ||
  fail 'generation failed; the checked-in data is unchanged'

count_models() { jq '.models | length' "$1"; }
count_ladders() { jq '[.models[] | select((.reasoning.levels // []) | length > 0)] | length' "$1"; }
list_ids() { jq -r '.models[].id' "$1" | sort; }

total_before=0 total_after=0 ladders_before=0 ladders_after=0 added_total=0 removed_total=0
printf '\n%-16s %-18s %-18s %s\n' provider models ladders 'added / retired'
for current in "$catalog_dir"/configs/*.json; do
  provider=$(basename "$current" .json)
  previous=$previous_dir/$provider.json
  [[ -f "$previous" ]] || previous=/dev/null

  if [[ "$previous" == /dev/null ]]; then
    models_was=0 ladders_was=0
  else
    models_was=$(count_models "$previous")
    ladders_was=$(count_ladders "$previous")
  fi
  models_now=$(count_models "$current")
  ladders_now=$(count_ladders "$current")

  if [[ "$previous" == /dev/null ]]; then
    added=$(list_ids "$current" | wc -l | tr -d ' ')
    removed=0
  else
    added=$(comm -13 <(list_ids "$previous") <(list_ids "$current") | wc -l | tr -d ' ')
    removed=$(comm -23 <(list_ids "$previous") <(list_ids "$current") | wc -l | tr -d ' ')
  fi

  total_before=$((total_before + models_was))
  total_after=$((total_after + models_now))
  ladders_before=$((ladders_before + ladders_was))
  ladders_after=$((ladders_after + ladders_now))
  added_total=$((added_total + added))
  removed_total=$((removed_total + removed))

  if [[ "$models_was" != "$models_now" || "$ladders_was" != "$ladders_now" ]]; then
    printf '%-16s %-18s %-18s +%s / -%s\n' \
      "$provider" "$models_was -> $models_now" "$ladders_was -> $ladders_now" "$added" "$removed"
  fi
done
printf '%-16s %-18s %-18s +%s / -%s\n' \
  TOTAL "$total_before -> $total_after" "$ladders_before -> $ladders_after" "$added_total" "$removed_total"

# A reasoning model without a ladder is expected — neither source publishes one
# for every model — but a jump in the count is worth a look before committing.
missing=0
for current in "$catalog_dir"/configs/*.json; do
  missing=$((missing + $(jq '[.models[] | select((.reasoning.supported // false) and ((.reasoning.levels // []) | length == 0) and ((.deprecated // false) | not))] | length' "$current")))
done
printf '\n%s reasoning models carry no effort ladder (neither source publishes one)\n' "$missing"

printf '\n── checking models/catalog\n'
(cd "$root" && MODULE=models/catalog scripts/check.sh)
(cd "$root" && MODULE=models/catalog scripts/check.sh race)

printf '\nreview with: git diff --stat models/catalog\n'
