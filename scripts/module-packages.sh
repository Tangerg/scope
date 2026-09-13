#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
template='{{.ImportPath}}'
if [[ "${1:-}" == "--buildable" ]]; then
  template='{{if or .GoFiles .CgoFiles}}{{.ImportPath}}{{end}}'
  shift
fi
module=${1:-}

if [[ -z "$module" ]]; then
  echo "usage: $0 <workspace-module>" >&2
  exit 2
fi

workspace_modules=$("$root/scripts/workspace-modules.sh")
known=0
while IFS= read -r workspace_module; do
  if [[ "$workspace_module" == "$module" ]]; then
    known=1
  fi
done <<< "$workspace_modules"
if [[ $known -eq 0 ]]; then
  echo "unknown workspace module: $module" >&2
  exit 2
fi

module_dir=$root
if [[ "$module" != "." ]]; then
  module_dir="$root/$module"
fi

# Go's ./... pattern does not treat node_modules as a boundary. A frontend
# dependency can therefore contribute incidental Go source to a Wails module.
# Resolve the module's packages once and keep third-party frontend trees out of
# every build, vet, test, race, and vulnerability gate.
packages=$(cd "$module_dir" && go list -f "$template" ./...)
while IFS= read -r package; do
  case "$package" in
    */node_modules/*) ;;
    *) printf '%s\n' "$package" ;;
  esac
done <<< "$packages"
