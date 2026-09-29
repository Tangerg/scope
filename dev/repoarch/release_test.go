package repoarch

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type moduleChecksum struct {
	hash   string
	source string
}

func TestReleaseEntryDerivesModulesAndKeepsTagsImmutable(t *testing.T) {
	t.Parallel()
	path := filepath.Join(repositoryRoot(t), "scripts", "release.sh")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Error("scripts/release.sh must be executable")
	}
	if output, syntaxErr := exec.Command("bash", "-n", path).CombinedOutput(); syntaxErr != nil {
		t.Fatalf("scripts/release.sh syntax: %v\n%s", syntaxErr, output)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	for _, required := range []string{
		"go work edit -json",
		"mod edit -json",
		"depth[path] + 0",
		"release plan has invalid layer",
		"release plan has no modules in layer",
		"scripts/check.sh build vet test race lint",
		"test -run '^$' ./...",
		"go clean -modcache",
		"git tag -a",
		"mod download -json",
		"refs/tags/$release_tag:refs/tags/$release_tag",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("scripts/release.sh no longer contains required release boundary %q", required)
		}
	}
	for _, forbidden := range []string{
		"scripts/check.sh build vet test race tidy lint",
		"tag -d",
		"--force",
		"push -f",
		"push --tags",
	} {
		if strings.Contains(script, forbidden) {
			t.Errorf("scripts/release.sh contains mutable or aggregate tag operation %q", forbidden)
		}
	}
}

func TestInternalModuleChecksumsAreConsistent(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	checksums := make(map[string]moduleChecksum)
	for _, module := range discoverModules(t, root) {
		path := filepath.Join(root, filepath.FromSlash(module.dir), "go.sum")
		file, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatal(err)
		}
		scanModuleChecksums(t, file, path, checksums)
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReleasePublishesNotesFromVerifiedModuleTags(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"new", "resume", "existing"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newReleasePublicationFixture(t, mode, "")
			output, err := fixture.run(t, "release.sh", "v0.0.2")
			if err != nil {
				t.Fatalf("release: %v\n%s\ncalls:\n%s", err, output, fixture.calls(t))
			}
			data, err := os.ReadFile(filepath.Join(fixture.root, "published-notes"))
			if err != nil {
				t.Fatal(err)
			}
			const want = "## Modules\n\n| Module | Tag | Commit |\n| --- | --- | --- |\n" +
				"| `github.com/Tangerg/scope/core` | [core/v0.0.2](https://github.com/fixture/scope/tree/core/v0.0.2) | [1111111111111111111111111111111111111111](https://github.com/fixture/scope/commit/1111111111111111111111111111111111111111) |\n" +
				"| `github.com/Tangerg/scope/consumer` | [consumer/v0.0.2](https://github.com/fixture/scope/tree/consumer/v0.0.2) | [2222222222222222222222222222222222222222](https://github.com/fixture/scope/commit/2222222222222222222222222222222222222222) |\n"
			if string(data) != want {
				t.Fatalf("notes must derive each module's own tagged commit:\n%s", data)
			}
			calls := fixture.calls(t)
			action := "create"
			if mode == "existing" {
				action = "edit"
			}
			if !strings.Contains(calls, "gh release "+action+" core/v0.0.2 --repo github.com/fixture/scope --verify-tag --notes-file ") ||
				!strings.Contains(calls, "--title Scope v0.0.2 --latest") {
				t.Fatalf("release must use the verified origin and existing module tag:\n%s", calls)
			}
			if mode != "new" && (strings.Contains(calls, "git push") || strings.Contains(calls, "repository-gates") || strings.Contains(calls, "git diff --name-only")) {
				t.Fatalf("published tags must resume from their own source without staging or pushing:\n%s", calls)
			}
			publishedAt := strings.Index(calls, "gh release "+action)
			for _, module := range []string{"core", "consumer"} {
				download := "public-download github.com/Tangerg/scope/" + module + "@v0.0.2"
				verifiedAt := strings.Index(calls, download)
				if verifiedAt < 0 || verifiedAt > publishedAt {
					t.Fatalf("%s must be remotely downloadable before publishing notes:\n%s", module, calls)
				}
			}
		})
	}
}

func TestReleaseDoesNotPublishUnverifiedNotes(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		mode     string
		boundary string
		message  string
	}{
		{mode: "new", boundary: "remote-mismatch", message: "remote verification failed for core/v0.0.2"},
		{mode: "resume", boundary: "remote-mismatch", message: "remote verification failed for core/v0.0.2"},
		{mode: "new", boundary: "public-download", message: "published-download-failed"},
		{mode: "resume", boundary: "public-download", message: "published-download-failed"},
		{mode: "resume", boundary: "archive-mismatch", message: "published archive differs from its tagged source"},
		{mode: "resume", boundary: "release-list", message: "release-list-failed"},
		{mode: "resume", boundary: "invalid-list", message: "invalid GitHub release list"},
		{mode: "resume", boundary: "different-push-repository", message: "fetch and push repositories differ"},
		{mode: "resume", boundary: "multiple-fetch-urls", message: "must have exactly one fetch URL"},
		{mode: "resume", boundary: "multiple-push-urls", message: "must have exactly one push URL"},
		{mode: "resume", boundary: "repository-lookup", message: "repository-lookup-failed"},
	} {
		t.Run(test.mode+"/"+test.boundary, func(t *testing.T) {
			fixture := newReleasePublicationFixture(t, test.mode, test.boundary)
			output, err := fixture.run(t, "release.sh", "v0.0.2")
			if err == nil || !strings.Contains(output, test.message) || strings.Contains(fixture.calls(t), "gh release ") {
				t.Fatalf("%s must stop before release publication: %v\n%s\ncalls:\n%s", test.boundary, err, output, fixture.calls(t))
			}
		})
	}
}

func TestReleasePreservesGitHubPublicationFailures(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"new", "resume", "existing"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newReleasePublicationFixture(t, mode, "release-write")
			output, err := fixture.run(t, "release.sh", "v0.0.2")
			if err == nil || !strings.Contains(output, "release-write-failed") ||
				strings.Contains(output, "release: published") || strings.Contains(output, "release: refreshed") {
				t.Fatalf("failed GitHub publication must remain a release failure: %v\n%s", err, output)
			}
		})
	}
}

func newReleasePublicationFixture(t *testing.T, mode, boundary string) scriptFailureFixture {
	t.Helper()
	fixture := newScriptFailureFixture(t, "release.sh")
	fixture.env = append(fixture.env, "SCRIPT_MODE="+mode, "SCRIPT_FAILURE="+boundary)
	fixture.write(t, "scripts/check.sh", "#!/usr/bin/env bash\necho repository-gates >> \"$SCRIPT_CALLS\"\n")
	fixture.write(t, "bin/go", `#!/usr/bin/env bash
set -euo pipefail
printf 'go %s\n' "$*" >> "$SCRIPT_CALLS"
case "$*" in
  'env GOMODCACHE') echo "$SCRIPT_ROOT/cache" ;;
  'env GOPROXY') echo off ;;
  'work edit -json') echo '{"Use":[{"DiskPath":"./core"},{"DiskPath":"./consumer"}]}' ;;
  *'list -m -f {{.Path}}') printf 'github.com/Tangerg/scope/%s\n' "${2##*/}" ;;
  *'mod edit -json')
    if [[ "$2" == */consumer ]]; then echo '{"Require":[{"Path":"github.com/Tangerg/scope/core"}]}'
    else echo '{"Require":[]}'; fi
    ;;
  *'mod edit -require='*|*'mod tidy'|*'mod tidy -diff'|*'test -run ^$ ./...'|'clean -modcache') ;;
  'mod download -json '*)
    sum=fixture
    if [[ "$GOMODCACHE" == */published-modcache ]]; then
      [[ -z "${GIT_CONFIG_VALUE_0:-}" ]] || { echo published-download-used-local-source >&2; exit 42; }
      printf 'public-download %s\n' "$4" >> "$SCRIPT_CALLS"
      if [[ "$SCRIPT_FAILURE" == public-download ]]; then echo published-download-failed >&2; exit 23; fi
      if [[ "$SCRIPT_FAILURE" == archive-mismatch ]]; then sum=different; fi
    fi
    printf '{"Path":"%s","Version":"%s","Sum":"%s","GoModSum":"fixture"}\n' "${4%@*}" "${4##*@}" "$sum"
    ;;
  *) echo "unexpected-go:$*" >&2; exit 42 ;;
esac
`)
	fixture.write(t, "bin/git", `#!/usr/bin/env bash
set -euo pipefail
printf 'git %s\n' "$*" >> "$SCRIPT_CALLS"
commit_for() {
  if [[ "$1" == core/* ]]; then echo 1111111111111111111111111111111111111111
  else echo 2222222222222222222222222222222222222222; fi
}
object_for() {
  if [[ "$1" == core/* ]]; then echo aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  else echo bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb; fi
}
case "$*" in
  'branch --show-current') echo main ;;
  'remote get-url --all origin')
    echo https://github.com/Fixture/scope.git
    if [[ "$SCRIPT_FAILURE" == multiple-fetch-urls ]]; then echo https://github.com/upstream/scope.git; fi
    ;;
  'remote get-url --push --all origin')
    if [[ "$SCRIPT_FAILURE" == multiple-push-urls ]]; then echo git@github.com:another/scope.git; fi
    if [[ "$SCRIPT_FAILURE" == different-push-repository ]]; then echo git@github.com:upstream/scope.git
    else echo git@github.com:fixture/scope.git; fi
    ;;
  'status --porcelain'|'fetch --prune --tags origin'|'merge-base --is-ancestor '*|'diff --cached --quiet'|'diff --quiet '*|'add -- '*) ;;
  'rev-parse origin/main') echo 2222222222222222222222222222222222222222 ;;
  'tag --list '*) printf '%s/v0.0.1\n' "${3%%/*}" ;;
  'rev-parse -q --verify refs/tags/'*)
    tag=${4#refs/tags/}
    [[ "$SCRIPT_MODE" != new || -f "$SCRIPT_ROOT/tag-${tag%%/*}" ]] || exit 1
    object_for "$tag"
    ;;
  'cat-file -t '*) echo tag ;;
  'rev-parse '*) object_for "$2" ;;
  'rev-list -n 1 '*) commit_for "$4" ;;
  'tag -a '*) touch "$SCRIPT_ROOT/tag-${3%%/*}" ;;
  'push origin main') ;;
  'push origin refs/tags/'*)
    tag=${3#refs/tags/}
    touch "$SCRIPT_ROOT/remote-${tag%%/*}"
    ;;
  'ls-remote --tags origin refs/tags/'*)
    tag=${4#refs/tags/}
    printf '%s\t%s\n' "$(commit_for "$tag")" "$4"
    ;;
  'ls-remote --tags origin')
    count=0
    if [[ -f "$SCRIPT_ROOT/remote-count" ]]; then read -r count < "$SCRIPT_ROOT/remote-count"; fi
    count=$((count+1))
    echo "$count" > "$SCRIPT_ROOT/remote-count"
    for module in core consumer; do
      [[ "$SCRIPT_MODE" != new || -f "$SCRIPT_ROOT/remote-$module" ]] || continue
      tag=$module/v0.0.2
      commit=$(commit_for "$tag")
      if [[ "$SCRIPT_FAILURE" == remote-mismatch && "$count" -gt 1 && "$module" == core ]]; then
        commit=3333333333333333333333333333333333333333
      fi
      printf '%s\trefs/tags/%s\n%s\trefs/tags/%s^{}\n' "$(object_for "$tag")" "$tag" "$commit" "$tag"
    done
    ;;
  *) echo "unexpected-git:$*" >&2; exit 42 ;;
esac
`)
	fixture.write(t, "bin/gh", `#!/usr/bin/env bash
set -euo pipefail
printf 'gh %s\n' "$*" >> "$SCRIPT_CALLS"
case "$*" in
  'repo view https://github.com/fixture/scope --json nameWithOwner')
    if [[ "$SCRIPT_FAILURE" == repository-lookup ]]; then echo repository-lookup-failed >&2; exit 23; fi
    echo '{"nameWithOwner":"fixture/scope"}'
    ;;
  'api --hostname github.com --paginate repos/fixture/scope/releases?per_page=100')
    echo '[]'
    if [[ "$SCRIPT_FAILURE" == release-list ]]; then echo release-list-failed >&2; exit 23; fi
    if [[ "$SCRIPT_FAILURE" == invalid-list ]]; then echo '{}'; fi
    if [[ "$SCRIPT_MODE" == existing ]]; then echo '[{"tag_name":"core/v0.0.2"}]'; fi
    ;;
  'release create '*|'release edit '*)
    if [[ "$SCRIPT_FAILURE" == release-write ]]; then echo release-write-failed >&2; exit 23; fi
    while (( $# > 0 )); do
      if [[ "$1" == --notes-file ]]; then cp "$2" "$SCRIPT_ROOT/published-notes"; break; fi
      shift
    done
    ;;
  *) echo "unexpected-gh:$*" >&2; exit 42 ;;
esac
`)
	return fixture
}

func scanModuleChecksums(
	t *testing.T,
	file *os.File,
	path string,
	checksums map[string]moduleChecksum,
) {
	t.Helper()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 3 || !isRepositoryImport(fields[0]) {
			continue
		}
		identity := fields[0] + " " + fields[1]
		previous, present := checksums[identity]
		if present && previous.hash != fields[2] {
			t.Errorf(
				"%s has checksum %s in %s and %s in %s",
				identity, previous.hash, previous.source, fields[2], path,
			)
			continue
		}
		checksums[identity] = moduleChecksum{hash: fields[2], source: path}
	}
	if err := scanner.Err(); err != nil {
		t.Errorf("scan %s: %v", path, err)
	}
}
