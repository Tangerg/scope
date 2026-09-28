#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <base-revision> <head-revision>" >&2
  exit 2
fi

root=$(cd "$(dirname "$0")/.." && pwd)
base=$1
head=$2
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

"$root/scripts/workspace-modules.sh" > "$work/modules"

while IFS= read -r module; do
  [[ -z "$module" ]] && continue
  module_path=$(awk '$1 == "module" { print $2; exit }' "$root/$module/go.mod")
  if [[ -z "$module_path" ]]; then
    echo "module path missing from $module/go.mod" >&2
    exit 1
  fi
  printf '%s\t%s\n' "$module_path" "$module" >> "$work/module-paths"
  printf '%08d\t%s\n' "${#module}" "$module" >> "$work/modules-by-depth"
done < "$work/modules"
sort -rn "$work/modules-by-depth" -o "$work/modules-by-depth"

git -C "$root" diff --name-only --diff-filter=ACDMRTUXB "$base...$head" > "$work/changed-files"

select_all=0
while IFS= read -r changed; do
  [[ -z "$changed" ]] && continue
  case "$changed" in
    go.work|go.work.sum|.golangci.yml|.github/*|scripts/*)
      select_all=1
      break
      ;;
  esac

  owner=
  while IFS=$'\t' read -r _ module; do
    case "$changed" in
      "$module"|"$module"/*)
        owner=$module
        break
        ;;
    esac
  done < "$work/modules-by-depth"
  [[ -z "$owner" ]] || printf '%s\n' "$owner" >> "$work/selected"
done < "$work/changed-files"

if [[ $select_all -eq 1 ]]; then
  cat "$work/modules"
  exit 0
fi

touch "$work/selected"
sort -u "$work/selected" -o "$work/selected"

# Record the workspace module graph as consumer<TAB>dependency. The reverse
# closure ensures a contract change also checks every module that compiles
# against it, while an isolated provider change stays isolated.
while IFS=$'\t' read -r _ module; do
  requirements=$(cd "$root/$module" && go mod edit -json | jq -r '.Require[]?.Path')
  while IFS= read -r requirement; do
    [[ -z "$requirement" ]] && continue
    dependency=$(awk -F '\t' -v path="$requirement" '$1 == path { print $2; exit }' "$work/module-paths")
    [[ -z "$dependency" ]] || printf '%s\t%s\n' "$module" "$dependency" >> "$work/dependencies"
  done <<< "$requirements"
done < "$work/modules-by-depth"
touch "$work/dependencies"

while :; do
  cp "$work/selected" "$work/selected-before"
  while IFS=$'\t' read -r consumer dependency; do
    if grep -Fqx "$dependency" "$work/selected"; then
      printf '%s\n' "$consumer" >> "$work/selected"
    fi
  done < "$work/dependencies"
  sort -u "$work/selected" -o "$work/selected"
  cmp -s "$work/selected-before" "$work/selected" && break
done

cat "$work/selected"
