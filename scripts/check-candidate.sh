#!/usr/bin/env bash
# Test the current, unpublished workspace through ordinary module downloads.
# No tags, replacements, GitHub writes or edits to the checkout are needed.
set -euo pipefail
root=$(git rev-parse --show-toplevel)
tmp_root=${TMPDIR:-/tmp}
tmp=$(mktemp -d "${tmp_root%/}/scope-candidate.XXXXXXXX")
trap 'rm -rf -- "$tmp"' EXIT
mkdir -p "$tmp/tree" "$tmp/proxy"
# A separate index captures current file contents without staging the user's work.
GIT_INDEX_FILE="$tmp/index" git -C "$root" read-tree --empty
git -C "$root" ls-files --cached --others --exclude-standard -z |
  GIT_INDEX_FILE="$tmp/index" git -C "$root" update-index --add --remove -z --stdin
rev=$(GIT_INDEX_FILE="$tmp/index" git -C "$root" write-tree)
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
for mod in "${modules[@]}"; do
  if ! grep -Fqx -- "$mod" "$tmp/modules"; then echo "unknown workspace module: $mod" >&2; exit 2; fi
done
# Repository checks inspect the tracked file set. Give the archive its own
# index rather than pointing any git command back at the developer's checkout.
git -C "$tmp/tree" init -q
git -C "$tmp/tree" add -f -- .
# Generated module versions do not describe a published repository commit.
export GOFLAGS="${GOFLAGS:+$GOFLAGS }-buildvcs=false"
export GOPROXY="file://$tmp/proxy,$(go env GOPROXY)"
export GONOSUMDB="github.com/Tangerg/scope,$(go env GONOSUMDB)" GONOPROXY=none GOWORK=off
# release.sh tidies after rewriting peers: a new peer can introduce external
# dependencies that the published versions never loaded. Normalize only these
# disposable manifests, before any test can inspect a sibling consumer.
while IFS= read -r member; do
  (cd "$tmp/tree/$member" && go mod tidy)
done < "$tmp/modules"
failed=()
for mod in "${modules[@]}"; do
  echo "candidate $version: $mod"
  if ! (cd "$tmp/tree/$mod" && go test -mod=readonly -count=1 ./...); then
    failed+=("$mod")
  fi
done
if [[ ${#failed[@]} -gt 0 ]]; then
  printf 'candidate checks failed: %s\n' "${failed[*]}" >&2
  exit 1
fi
