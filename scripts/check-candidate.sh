#!/usr/bin/env bash
# Test the staged, unpublished workspace through ordinary module downloads.
# No tags, replacements, GitHub writes or edits to the checkout are needed.
set -euo pipefail
root=$(git rev-parse --show-toplevel)
rev=$(git -C "$root" write-tree)
tmp_root=${TMPDIR:-/tmp}
tmp=$(mktemp -d "${tmp_root%/}/scope-candidate.XXXXXXXX")
trap 'rm -rf -- "$tmp"' EXIT
mkdir -p "$tmp/tree" "$tmp/proxy"
git -C "$root" archive "$rev" | tar -x -C "$tmp/tree"
version="v0.0.0-candidate.h$rev"
(cd "$tmp/tree/dev/repoarch" && GOWORK=off go build -o "$tmp/stage" "$tmp/tree/scripts/candidateproxy.go")
"$tmp/stage" "$tmp/tree" "$tmp/proxy" "$version" > "$tmp/modules"
modules=()
if (( $# == 0 )); then
  while IFS= read -r module; do modules+=("$module"); done < "$tmp/modules"
else
  modules=("$@")
fi
# Repository checks inspect the tracked file set. Give the archive its own
# index rather than pointing any git command back at the developer's checkout.
git -C "$tmp/tree" init -q
git -C "$tmp/tree" add -f -- .
# Generated module versions do not describe a published repository commit.
export GOFLAGS="${GOFLAGS:+$GOFLAGS }-buildvcs=false"
export GOPROXY="file://$tmp/proxy,$(go env GOPROXY)"
export GONOSUMDB="github.com/Tangerg/scope,$(go env GONOSUMDB)" GONOPROXY=none GOWORK=off
# A module's tests can inspect a sibling consumer before that sibling runs.
# Resolve its declared candidate versions with Go's own checksum verifier.
while IFS= read -r member; do
  (
    cd "$tmp/tree/$member"
    direct=$(go mod edit -json | jq -r --arg v "$version" '.Require[]? | select(.Version == $v) | .Path')
    if [[ -n "$direct" ]]; then
      peers=()
      while IFS= read -r peer; do peers+=("$peer"); done <<< "$direct"
      go mod download "${peers[@]}"
    fi
  )
done < "$tmp/modules"
for mod in "${modules[@]}"; do
  if ! grep -Fqx -- "$mod" "$tmp/modules"; then echo "unknown workspace module: $mod" >&2; exit 2; fi
  echo "candidate $version: $mod"
  (cd "$tmp/tree/$mod" && go test -mod=mod -count=1 ./...)
done
