#!/usr/bin/env bash
set -euo pipefail

release_root=$(cd "$(dirname "$0")/.." && pwd)
release_remote=origin
release_branch=main
release_module_prefix=github.com/Tangerg/scope/

fail() {
  printf 'release: %s\n' "$*" >&2
  exit 1
}

usage() {
  printf 'usage: scripts/release.sh vMAJOR.MINOR.PATCH\n' >&2
  exit 2
}

cleanup() {
  if [[ -n "${release_temporary_dir:-}" &&
    "$release_temporary_dir" == /tmp/scope-release.* &&
    -d "$release_temporary_dir" ]]; then
    local cache
    for cache in "$release_temporary_dir/modcache" "$release_temporary_dir/published-modcache"; do
      [[ ! -d "$cache" ]] || env GOWORK=off GOMODCACHE="$cache" go clean -modcache
    done
    rm -rf -- "$release_temporary_dir"
  fi
}

trap cleanup EXIT

[[ $# -eq 1 ]] || usage
release_version=$1
[[ "$release_version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || usage

for release_command in go git gh jq awk sed; do
  command -v "$release_command" >/dev/null || fail "$release_command is required"
done

cd "$release_root"
[[ $(git branch --show-current) == "$release_branch" ]] || fail "run from branch $release_branch"
release_status=$(git status --porcelain)
[[ -z "$release_status" ]] || fail "working tree must be clean"

github_repository() {
  local url=$1
  local repository
  case "$url" in
    https://github.com/*) repository=${url#https://github.com/} ;;
    git@github.com:*) repository=${url#git@github.com:} ;;
    ssh://git@github.com/*) repository=${url#ssh://git@github.com/} ;;
    *) fail "$release_remote must use a GitHub repository URL" ;;
  esac
  repository=${repository%/}
  repository=${repository%.git}
  [[ "$repository" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] ||
    fail "$release_remote has an invalid GitHub repository path"
  printf '%s\n' "$repository" | tr '[:upper:]' '[:lower:]'
}

release_remote_url=$(git remote get-url --all "$release_remote")
[[ "$release_remote_url" != *$'\n'* ]] || fail "$release_remote must have exactly one fetch URL"
release_repository=$(github_repository "$release_remote_url")
release_push_url=$(git remote get-url --push --all "$release_remote")
[[ "$release_push_url" != *$'\n'* ]] || fail "$release_remote must have exactly one push URL"
release_push_repository=$(github_repository "$release_push_url")
[[ "$release_repository" == "$release_push_repository" ]] ||
  fail "$release_remote fetch and push repositories differ"
release_repository=$(gh repo view "https://github.com/$release_repository" --json nameWithOwner | jq -er '.nameWithOwner')
[[ "$release_repository" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] ||
  fail "$release_remote did not resolve to a GitHub repository"

git fetch --prune --tags "$release_remote"
release_remote_head=$(git rev-parse "$release_remote/$release_branch")
git merge-base --is-ancestor "$release_remote_head" HEAD ||
  fail "$release_remote/$release_branch is ahead of or diverged from local $release_branch"

release_temporary_dir=$(mktemp -d /tmp/scope-release.XXXXXX)
release_module_table=$release_temporary_dir/modules.tsv
release_edge_table=$release_temporary_dir/edges.tsv
release_plan=$release_temporary_dir/plan.tsv
release_remote_tags=$release_temporary_dir/remote-tags.tsv
release_existing_modcache=$(go env GOMODCACHE)
release_existing_proxy=$(go env GOPROXY)
release_modcache=$release_temporary_dir/modcache
release_published_modcache=$release_temporary_dir/published-modcache
mkdir -p "$release_modcache"
: >"$release_edge_table"

git ls-remote --tags "$release_remote" >"$release_remote_tags"

release_workspace_paths=$(go work edit -json | jq -r '.Use[].DiskPath')
while IFS= read -r release_disk_path; do
  [[ -z "$release_disk_path" ]] && continue
  [[ "$release_disk_path" == ./* && "$release_disk_path" != *'/../'* ]] ||
    fail "workspace module path is not repository-relative: $release_disk_path"
  release_module_dir=$release_root/${release_disk_path#./}
  release_module_path=$(env GOWORK=off go -C "$release_module_dir" list -m -f '{{.Path}}')
  [[ "$release_module_path" == "$release_module_prefix"* ]] ||
    fail "workspace module is outside $release_module_prefix: $release_module_path"
  printf '%s\t%s\n' "$release_module_path" "$release_module_dir" >>"$release_module_table"
done <<< "$release_workspace_paths"

[[ -s "$release_module_table" ]] || fail "go.work contains no releasable modules"
sort -o "$release_module_table" "$release_module_table"

while IFS=$'\t' read -r release_module_path release_module_dir; do
  release_requirements=$(env GOWORK=off go -C "$release_module_dir" mod edit -json | jq -r '.Require[]?.Path')
  while IFS= read -r release_dependency; do
    [[ -n "$release_dependency" ]] || continue
    if awk -F '\t' -v dependency="$release_dependency" '$1 == dependency { found = 1 } END { exit !found }' "$release_module_table"; then
      printf '%s\t%s\n' "$release_dependency" "$release_module_path" >>"$release_edge_table"
    fi
  done <<< "$release_requirements"
done <"$release_module_table"

awk -F '\t' '
  FNR == NR {
    directory[$1] = $2
    module[$1] = 1
    module_count++
    next
  }
  {
    edge_count++
    dependency[edge_count] = $1
    dependent[edge_count] = $2
  }
  END {
    for (iteration = 1; iteration < module_count; iteration++) {
      changed = 0
      for (edge = 1; edge <= edge_count; edge++) {
        candidate = depth[dependency[edge]] + 1
        if (depth[dependent[edge]] < candidate) {
          depth[dependent[edge]] = candidate
          changed = 1
        }
      }
      if (!changed) {
        break
      }
    }
    for (edge = 1; edge <= edge_count; edge++) {
      if (depth[dependent[edge]] < depth[dependency[edge]] + 1) {
        print "release: internal module dependency cycle" > "/dev/stderr"
        exit 1
      }
    }
    for (path in module) {
      print (depth[path] + 0) "\t" path "\t" directory[path]
    }
  }
' "$release_module_table" "$release_edge_table" | sort -t $'\t' -k1,1n -k2,2 >"$release_plan"

release_plan_count=0
while IFS=$'\t' read -r release_layer release_module_path release_module_dir; do
  [[ "$release_layer" =~ ^[0-9]+$ ]] || fail "release plan has invalid layer: $release_layer"
  [[ "$release_module_path" == "$release_module_prefix"* ]] ||
    fail "release plan has invalid module: $release_module_path"
  [[ "$release_module_dir" == "$release_root"/* && -f "$release_module_dir/go.mod" ]] ||
    fail "release plan has invalid directory: $release_module_dir"
  release_plan_count=$((release_plan_count + 1))
done <"$release_plan"
release_module_table_count=$(wc -l <"$release_module_table" | tr -d ' ')
((release_plan_count == release_module_table_count)) ||
  fail "release plan covers $release_plan_count of $release_module_table_count modules"

version_greater_than() {
  local candidate=${1#v}
  local reference=${2#v}
  local candidate_major candidate_minor candidate_patch
  local reference_major reference_minor reference_patch
  IFS=. read -r candidate_major candidate_minor candidate_patch <<<"$candidate"
  IFS=. read -r reference_major reference_minor reference_patch <<<"$reference"
  if ((candidate_major != reference_major)); then
    ((candidate_major > reference_major))
    return
  fi
  if ((candidate_minor != reference_minor)); then
    ((candidate_minor > reference_minor))
    return
  fi
  ((candidate_patch > reference_patch))
}

tag_for() {
  local module_path=$1
  printf '%s/%s\n' "${module_path#"$release_module_prefix"}" "$release_version"
}

remote_tag_object() {
  local tag=$1
  awk -v reference="refs/tags/$tag" '$2 == reference { print $1; exit }' "$release_remote_tags"
}

remote_tag_commit() {
  local tag=$1
  local peeled
  peeled=$(awk -v reference="refs/tags/$tag^{}" '$2 == reference { print $1; exit }' "$release_remote_tags")
  if [[ -n "$peeled" ]]; then
    printf '%s\n' "$peeled"
    return
  fi
  remote_tag_object "$tag"
}

release_go() {
  env \
    GOWORK=off \
    GOMODCACHE="$release_modcache" \
    GONOPROXY=github.com/Tangerg/scope \
    GONOSUMDB=github.com/Tangerg/scope \
    GOPROXY="file://$release_existing_modcache/cache/download,$release_existing_proxy" \
    GIT_CONFIG_COUNT=1 \
    GIT_CONFIG_KEY_0="url.file://$release_root/.insteadOf" \
    GIT_CONFIG_VALUE_0=https://github.com/Tangerg/scope \
    go "$@"
}

publish_release() {
  local tag commit module_path module_dir layer
  local anchor=core/$release_version
  local notes=$release_temporary_dir/notes.md
  local local_download=$release_temporary_dir/local-download.json
  local published_download=$release_temporary_dir/published-download.json
  local releases=$release_temporary_dir/releases.json
  local existing

  git ls-remote --tags "$release_remote" >"$release_remote_tags"
  printf '## Modules\n\n| Module | Tag | Commit |\n| --- | --- | --- |\n' >"$notes"
  while IFS=$'\t' read -r layer module_path module_dir; do
    tag=$(tag_for "$module_path")
    commit=$(git rev-list -n 1 "$tag")
    [[ "$(remote_tag_object "$tag")" == "$(git rev-parse "$tag")" &&
      "$(remote_tag_commit "$tag")" == "$commit" ]] ||
      fail "remote verification failed for $tag"

    release_go mod download -json "$module_path@$release_version" >"$local_download"
    env GOWORK=off GOMODCACHE="$release_published_modcache" GOPROXY="$release_existing_proxy" \
      go mod download -json "$module_path@$release_version" >"$published_download"
    jq -e --arg module "$module_path" --arg version "$release_version" --slurpfile local "$local_download" '
      .Error == null and .Path == $module and .Version == $version and
      (.Sum | type == "string" and length > 0) and (.GoModSum | type == "string" and length > 0) and
      ($local | length == 1) and $local[0].Error == null and
      .Sum == $local[0].Sum and .GoModSum == $local[0].GoModSum
    ' "$published_download" >/dev/null || fail "$tag published archive differs from its tagged source"

    printf '| `%s` | [%s](https://github.com/%s/tree/%s) | [%s](https://github.com/%s/commit/%s) |\n' \
      "$module_path" "$tag" "$release_repository" "$tag" "$commit" "$release_repository" "$commit" >>"$notes"
  done <"$release_plan"

  [[ -n "$(remote_tag_object "$anchor")" ]] || fail "release anchor $anchor is not published"
  gh api --hostname github.com --paginate "repos/$release_repository/releases?per_page=100" >"$releases"
  existing=$(jq -ser --arg tag "$anchor" '
    if length > 0 and all(.[]; type == "array") then
      [.[][] | select(.tag_name == $tag)] | length
    else error("invalid GitHub release list") end
  ' "$releases")
  case "$existing" in
    0) gh release create "$anchor" --repo "github.com/$release_repository" \
      --verify-tag --notes-file "$notes" --title "Scope $release_version" --latest ;;
    1) gh release edit "$anchor" --repo "github.com/$release_repository" \
      --verify-tag --notes-file "$notes" --title "Scope $release_version" --latest --draft=false --prerelease=false ;;
    *) fail "multiple GitHub releases reference $anchor" ;;
  esac
}

release_module_count=0
release_remote_count=0
release_first_existing_tag=
while IFS=$'\t' read -r release_layer release_module_path release_module_dir; do
  release_module_count=$((release_module_count + 1))
  release_tag=$(tag_for "$release_module_path")
  release_latest=
  release_candidate_tags=$(git tag --list "${release_tag%/*}/v*" --sort=-version:refname)
  while IFS= read -r release_candidate_tag; do
    [[ "$release_candidate_tag" == "$release_tag" ]] && continue
    if [[ "$release_candidate_tag" =~ /v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
      release_latest=${release_candidate_tag##*/}
      break
    fi
  done <<< "$release_candidate_tags"
  if [[ -n "$release_latest" ]] && ! version_greater_than "$release_version" "$release_latest"; then
    fail "$release_tag must be greater than ${release_tag%/*}/$release_latest"
  fi

  release_remote_object=$(remote_tag_object "$release_tag")
  if git rev-parse -q --verify "refs/tags/$release_tag" >/dev/null; then
    [[ $(git cat-file -t "$release_tag") == tag ]] || fail "$release_tag is not annotated"
    release_first_existing_tag=${release_first_existing_tag:-$release_tag}
    if [[ -n "$release_remote_object" ]]; then
      [[ "$release_remote_object" == "$(git rev-parse "$release_tag")" ]] ||
        fail "local and remote $release_tag objects differ"
      [[ "$(remote_tag_commit "$release_tag")" == "$(git rev-list -n 1 "$release_tag")" ]] ||
        fail "local and remote $release_tag commits differ"
      release_remote_count=$((release_remote_count + 1))
    fi
  elif [[ -n "$release_remote_object" ]]; then
    fail "remote $release_tag exists but was not fetched locally"
  fi
done <"$release_plan"

if ((release_remote_count == release_module_count)); then
  publish_release
  printf 'release: refreshed %s for %d published modules\n' "$release_version" "$release_module_count"
  exit 0
fi

if [[ -n "$release_first_existing_tag" ]]; then
  release_existing_commit=$(git rev-list -n 1 "$release_first_existing_tag")
  git merge-base --is-ancestor "$release_existing_commit" HEAD ||
    fail "$release_first_existing_tag is not an ancestor of HEAD"
  release_changed_paths=$(git diff --name-only "$release_existing_commit" HEAD)
  while IFS= read -r release_changed_path; do
    [[ -z "$release_changed_path" ]] && continue
    case "$release_changed_path" in
      */go.mod | */go.sum) ;;
      *) fail "source changed after $release_first_existing_tag; use a new version" ;;
    esac
  done <<< "$release_changed_paths"
fi

printf 'release: running repository gates before freezing %s\n' "$release_version"
# Tidy belongs to the dependency layer below: a new package in an internal
# module cannot be resolved independently until that module's new tag exists.
scripts/check.sh build vet test race lint
release_status=$(git status --porcelain)
[[ -z "$release_status" ]] || fail "repository gates changed the working tree"

release_max_layer=$(awk -F '\t' 'END { print $1 }' "$release_plan")
for ((release_layer = 0; release_layer <= release_max_layer; release_layer++)); do
  release_stage_paths=()
  while IFS=$'\t' read -r release_planned_layer release_module_path release_module_dir; do
    [[ "$release_planned_layer" == "$release_layer" ]] || continue
    release_requirements=$(env GOWORK=off go -C "$release_module_dir" mod edit -json | jq -r '.Require[]?.Path')
    while IFS= read -r release_dependency; do
      [[ -n "$release_dependency" ]] || continue
      if awk -F '\t' -v dependency="$release_dependency" '$1 == dependency { found = 1 } END { exit !found }' "$release_module_table"; then
        env GOWORK=off go -C "$release_module_dir" mod edit -require="$release_dependency@$release_version"
      fi
    done <<< "$release_requirements"

    release_go -C "$release_module_dir" mod tidy
    release_go -C "$release_module_dir" mod tidy -diff
    # Repository-wide behavior was tested above; this pass proves that every
    # test package resolves through released module metadata without go.work.
    release_go -C "$release_module_dir" test -run '^$' ./...

    release_tag=$(tag_for "$release_module_path")
    if git rev-parse -q --verify "refs/tags/$release_tag" >/dev/null; then
      git diff --quiet "$release_tag" -- "$release_module_dir/go.mod" "$release_module_dir/go.sum" ||
        fail "$release_tag is immutable but its module metadata changed"
    fi
    release_stage_paths+=("$release_module_dir/go.mod")
    [[ -f "$release_module_dir/go.sum" ]] && release_stage_paths+=("$release_module_dir/go.sum")
  done <"$release_plan"

  ((${#release_stage_paths[@]} > 0)) || fail "release plan has no modules in layer $release_layer"
  git add -- "${release_stage_paths[@]}"
  if ! git diff --cached --quiet; then
    git commit -m "chore(release): pin layer $release_layer modules to $release_version"
  fi

  while IFS=$'\t' read -r release_planned_layer release_module_path release_module_dir; do
    [[ "$release_planned_layer" == "$release_layer" ]] || continue
    release_tag=$(tag_for "$release_module_path")
    if ! git rev-parse -q --verify "refs/tags/$release_tag" >/dev/null; then
      git tag -a "$release_tag" -m "Release $release_module_path $release_version"
    fi
    release_download=$release_temporary_dir/download.json
    release_go mod download -json "$release_module_path@$release_version" >"$release_download"
    jq -e '.Error == null and .Sum != null and .GoModSum != null' "$release_download" >/dev/null ||
      fail "$release_tag did not produce a complete Go module archive"
  done <"$release_plan"
done

release_status=$(git status --porcelain)
[[ -z "$release_status" ]] || fail "release staging left uncommitted changes"

# CI starts on the branch update and resolves the pinned requirements of the
# pushed commits, so the branch and every new tag must become visible together.
release_push_refs=("refs/heads/$release_branch:refs/heads/$release_branch")
while IFS=$'\t' read -r release_layer release_module_path release_module_dir; do
  release_tag=$(tag_for "$release_module_path")
  [[ -n "$(remote_tag_object "$release_tag")" ]] ||
    release_push_refs+=("refs/tags/$release_tag:refs/tags/$release_tag")
done <"$release_plan"
git push --atomic "$release_remote" "${release_push_refs[@]}"
while IFS=$'\t' read -r release_layer release_module_path release_module_dir; do
  release_tag=$(tag_for "$release_module_path")
  release_published=$(git ls-remote --tags "$release_remote" "refs/tags/$release_tag^{}" | awk 'NR == 1 { print $1 }')
  [[ "$release_published" == "$(git rev-list -n 1 "$release_tag")" ]] ||
    fail "remote verification failed for $release_tag"
done <"$release_plan"

publish_release
printf 'release: published %s for %d modules\n' "$release_version" "$release_module_count"
