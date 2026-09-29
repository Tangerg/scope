package repoarch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAffectedModulesRejectsIncompleteDependencyGraph(t *testing.T) {
	t.Parallel()
	fixture := newScriptFailureFixture(t, "affected-modules.sh")
	fixture.write(t, "scripts/workspace-modules.sh", "#!/usr/bin/env bash\nprintf '%s\\n' core consumer\n")
	fixture.write(t, "bin/git", "#!/usr/bin/env bash\necho core/source.go\n")
	fixture.write(t, "bin/go", `#!/usr/bin/env bash
printf '{"Require":[{"Path":"github.com/Tangerg/scope/core"}]}\n'
echo dependency-enumeration-failed >&2
exit 23
`)
	output, err := fixture.run(t, "affected-modules.sh", "base", "head")
	if err == nil || !strings.Contains(output, "dependency-enumeration-failed") {
		t.Fatalf("incomplete dependency enumeration must fail: %v\n%s", err, output)
	}
}

func TestVulnerabilityCheckRejectsIncompletePackageList(t *testing.T) {
	t.Parallel()
	fixture := newScriptFailureFixture(t, "check-vulnerabilities.sh")
	fixture.write(t, "scripts/module-packages.sh", `#!/usr/bin/env bash
echo github.com/Tangerg/scope/consumer
echo package-enumeration-failed >&2
exit 23
`)
	fixture.write(t, "bin/govulncheck", "#!/usr/bin/env bash\necho vulnerability-scan >> \"$SCRIPT_CALLS\"\necho '{}'\n")
	output, err := fixture.run(t, "check-vulnerabilities.sh", "consumer")
	if err == nil || !strings.Contains(output, "package-enumeration-failed") || strings.Contains(fixture.calls(t), "vulnerability-scan") {
		t.Fatalf("incomplete package enumeration must stop before scanning: %v\n%s", err, output)
	}
}

func TestVulnerabilityCheckRejectsUnreadableReport(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"reachable", "imported"} {
		t.Run(boundary, func(t *testing.T) {
			fixture := newScriptFailureFixture(t, "check-vulnerabilities.sh")
			fixture.write(t, "scripts/module-packages.sh", "#!/usr/bin/env bash\necho github.com/Tangerg/scope/consumer\n")
			fixture.write(t, "bin/govulncheck", "#!/usr/bin/env bash\necho '{}'\n")
			fixture.write(t, "bin/jq", `#!/usr/bin/env bash
if [[ "$SCRIPT_FAILURE" == reachable || "$*" == *'| not)'* ]]; then
  if [[ "$SCRIPT_FAILURE" == imported ]]; then echo GO-2026-9999; fi
  echo vulnerability-report-unreadable >&2
  exit 23
fi
exec "$SCRIPT_REAL_JQ" "$@"
`)
			fixture.env = append(fixture.env, "SCRIPT_FAILURE="+boundary)
			output, err := fixture.run(t, "check-vulnerabilities.sh", "consumer")
			if err == nil || !strings.Contains(output, "vulnerability-report-unreadable") || strings.Contains(output, "no reachable vulnerabilities") {
				t.Fatalf("unreadable %s findings must fail: %v\n%s", boundary, err, output)
			}
		})
	}
}

func TestVulnerabilityCheckAcceptsEmptyFindings(t *testing.T) {
	t.Parallel()
	fixture := newScriptFailureFixture(t, "check-vulnerabilities.sh")
	fixture.write(t, "scripts/module-packages.sh", "#!/usr/bin/env bash\necho github.com/Tangerg/scope/consumer\n")
	fixture.write(t, "bin/govulncheck", "#!/usr/bin/env bash\necho '{}'\n")
	output, err := fixture.run(t, "check-vulnerabilities.sh", "consumer")
	if err != nil || output != "consumer: no reachable vulnerabilities\n" {
		t.Fatalf("empty findings must remain a successful clean scan: %v\n%s", err, output)
	}
}

func TestReleaseRejectsIncompleteMetadataBeforeGates(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"workspace", "requirements", "tags", "status-1", "resume-diff"} {
		t.Run(boundary, func(t *testing.T) {
			fixture := newScriptFailureFixture(t, "release.sh")
			fixture.prepareRelease(t, boundary)
			output, err := fixture.run(t, "release.sh", "v0.0.2")
			if err == nil || !strings.Contains(output, "metadata-failed:"+boundary) || strings.Contains(fixture.calls(t), "repository-gates") {
				t.Fatalf("%s metadata failure must stop before gates: %v\n%s\ncalls:\n%s", boundary, err, output, fixture.calls(t))
			}
		})
	}
}

func TestReleaseRejectsIncompleteMetadataAfterGates(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		boundary  string
		forbidden string
	}{
		{boundary: "status-2", forbidden: "mod tidy"},
		{boundary: "requirements-2", forbidden: "mod tidy"},
		{boundary: "status-3", forbidden: "git push"},
	} {
		t.Run(test.boundary, func(t *testing.T) {
			fixture := newScriptFailureFixture(t, "release.sh")
			fixture.prepareRelease(t, test.boundary)
			output, err := fixture.run(t, "release.sh", "v0.0.2")
			calls := fixture.calls(t)
			if err == nil || !strings.Contains(output, "metadata-failed:"+test.boundary) ||
				!strings.Contains(calls, "repository-gates") || strings.Contains(calls, test.forbidden) {
				t.Fatalf("%s metadata failure must stop subsequent release work: %v\n%s\ncalls:\n%s", test.boundary, err, output, calls)
			}
		})
	}
}

type scriptFailureFixture struct {
	root string
	env  []string
}

func newScriptFailureFixture(t *testing.T, script string) scriptFailureFixture {
	t.Helper()
	root := t.TempDir()
	realJQ, err := exec.LookPath("jq")
	if err != nil {
		t.Fatal(err)
	}
	fixture := scriptFailureFixture{
		root: root,
		env: append(os.Environ(),
			"PATH="+filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
			"SCRIPT_ROOT="+root,
			"SCRIPT_CALLS="+filepath.Join(root, "calls"),
			"SCRIPT_REAL_JQ="+realJQ,
		),
	}
	data, err := os.ReadFile(filepath.Join(repositoryRoot(t), "scripts", script))
	if err != nil {
		t.Fatal(err)
	}
	fixture.write(t, "scripts/"+script, string(data))
	fixture.write(t, "core/go.mod", "module github.com/Tangerg/scope/core\n")
	fixture.write(t, "consumer/go.mod", "module github.com/Tangerg/scope/consumer\n")
	return fixture
}

func (s scriptFailureFixture) write(t *testing.T, name, content string) {
	t.Helper()
	path := filepath.Join(s.root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}

func (s scriptFailureFixture) run(t *testing.T, script string, args ...string) (string, error) {
	t.Helper()
	command := exec.CommandContext(t.Context(), "bash", append([]string{"scripts/" + script}, args...)...)
	command.Dir = s.root
	command.Env = s.env
	output, err := command.CombinedOutput()
	return string(output), err
}

func (s scriptFailureFixture) calls(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(s.root, "calls"))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func (s *scriptFailureFixture) prepareRelease(t *testing.T, boundary string) {
	t.Helper()
	s.env = append(s.env, "SCRIPT_FAILURE="+boundary)
	s.write(t, "bin/gh", `#!/usr/bin/env bash
if [[ "$*" == 'repo view https://github.com/tangerg/scope --json nameWithOwner' ]]; then
  echo '{"nameWithOwner":"Tangerg/scope"}'
  exit 0
fi
echo "unexpected-gh:$*" >&2
exit 42
`)
	s.write(t, "scripts/check.sh", `#!/usr/bin/env bash
echo repository-gates >> "$SCRIPT_CALLS"
case "$SCRIPT_FAILURE" in
  status-2|status-3|requirements-2) exit 0 ;;
  *) exit 41 ;;
esac
`)
	s.write(t, "bin/go", `#!/usr/bin/env bash
printf 'go %s\n' "$*" >> "$SCRIPT_CALLS"
case "$*" in
  'env GOMODCACHE') echo "$SCRIPT_ROOT/cache" ;;
  'env GOPROXY') echo off ;;
  'work edit -json')
    echo '{"Use":[{"DiskPath":"./consumer"}]}'
    if [[ "$SCRIPT_FAILURE" == workspace ]]; then echo metadata-failed:workspace >&2; exit 23; fi
    ;;
  *'list -m -f {{.Path}}') echo github.com/Tangerg/scope/consumer ;;
  *'mod edit -json')
    count=0
    if [[ -f "$SCRIPT_ROOT/requirements-count" ]]; then read -r count < "$SCRIPT_ROOT/requirements-count"; fi
    count=$((count+1))
    echo "$count" > "$SCRIPT_ROOT/requirements-count"
    echo '{"Require":[]}'
    if [[ "$SCRIPT_FAILURE" == requirements || "$SCRIPT_FAILURE" == requirements-2 && "$count" == 2 ]]; then
      echo "metadata-failed:$SCRIPT_FAILURE" >&2
      exit 23
    fi
    ;;
  *'mod tidy'|*'mod tidy -diff'|*'test -run ^$ ./...'|'clean -modcache') ;;
  'mod download -json '*) echo '{"Sum":"fixture","GoModSum":"fixture"}' ;;
  *) echo "unexpected-go:$*" >&2; exit 42 ;;
esac
`)
	s.write(t, "bin/git", `#!/usr/bin/env bash
printf 'git %s\n' "$*" >> "$SCRIPT_CALLS"
case "$*" in
  'branch --show-current') echo main ;;
  'remote get-url --all origin'|'remote get-url --push --all origin') echo https://github.com/Tangerg/scope.git ;;
  'status --porcelain')
    count=0
    if [[ -f "$SCRIPT_ROOT/status-count" ]]; then read -r count < "$SCRIPT_ROOT/status-count"; fi
    count=$((count+1))
    echo "$count" > "$SCRIPT_ROOT/status-count"
    if [[ "$SCRIPT_FAILURE" == "status-$count" ]]; then echo "metadata-failed:$SCRIPT_FAILURE" >&2; exit 23; fi
    ;;
  'fetch --prune --tags origin'|'ls-remote --tags origin'|'merge-base --is-ancestor '*) ;;
  'rev-parse origin/main') echo remote-head ;;
  'tag --list '*)
    echo consumer/v0.0.1
    if [[ "$SCRIPT_FAILURE" == tags ]]; then echo metadata-failed:tags >&2; exit 23; fi
    ;;
  'rev-parse -q --verify refs/tags/'*)
    if [[ "$SCRIPT_FAILURE" != resume-diff ]]; then exit 1; fi
    echo existing-tag
    ;;
  'cat-file -t '*) echo tag ;;
  'rev-list -n 1 '*) echo release-commit ;;
  'diff --name-only '*)
    echo consumer/go.mod
    echo metadata-failed:resume-diff >&2
    exit 23
    ;;
  'diff --cached --quiet'|'add -- '*|'tag -a '*) ;;
  'push '*) echo publication-boundary-reached >&2; exit 41 ;;
  *) echo "unexpected-git:$*" >&2; exit 42 ;;
esac
`)
}
