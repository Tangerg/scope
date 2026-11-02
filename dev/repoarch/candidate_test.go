package repoarch

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCandidateConsumesCurrentWorktreeWithoutChangingGitOrPins(t *testing.T) {
	fixture := newCandidateFixture(t)
	writeCandidateFile(t, fixture.root, "core/value.go", "package core\nconst Value = 99\n")
	writeCandidateFile(t, fixture.root, "consumer/deleted_test.go", "package consumer\nimport \"testing\"\nfunc TestDeleted(t *testing.T) { t.Fatal(\"deleted file was included\") }\n")
	if output, err := fixture.run(t, "", "git", "add", "core/value.go", "consumer/deleted_test.go"); err != nil {
		t.Fatalf("stage fixture changes: %v\n%s", err, output)
	}
	if err := os.Remove(filepath.Join(fixture.root, "consumer/deleted_test.go")); err != nil {
		t.Fatal(err)
	}
	writeCandidateFile(t, fixture.root, "core/value.go", "package core\nconst Value = 3\n")
	writeCandidateFile(t, fixture.root, "consumer/expected_test.go", "package consumer\nconst expectedCandidateValue = 3\n")
	writeCandidateFile(t, fixture.root, "consumer/data with spaces.txt", "current contents\n")
	writeCandidateFile(t, fixture.root, "consumer/ignored_test.go", "not Go source\n")
	writeCandidateFile(t, fixture.root, "consumer/value_test.go", `package consumer
import (
 "os"
 "os/exec"
 "strings"
 "testing"
 "github.com/Tangerg/scope/core"
)
func TestCandidateDependency(t *testing.T) {
 if core.Value != expectedCandidateValue {
  t.Fatalf("candidate value = %d, want %d", core.Value, expectedCandidateValue)
 }
 if os.Getenv("GOWORK") != "off" { t.Fatalf("workspace = %q", os.Getenv("GOWORK")) }
 command := exec.Command("go", "list", "-m", "-f", "{{.Main}}|{{if .Replace}}replaced{{end}}|{{.Version}}", "github.com/Tangerg/scope/core")
 output, err := command.CombinedOutput()
 if err != nil || !strings.HasPrefix(string(output), "false||v0.0.0-candidate.h") {
  t.Fatalf("candidate dependency must be an ordinary module: %v %s", err, output)
 }
 data, err := os.ReadFile("data with spaces.txt")
 if err != nil || string(data) != "current contents\n" { t.Fatalf("current new file = %q, %v", data, err) }
}
`)
	type fileSnapshot struct {
		path string
		data []byte
	}
	var protected []fileSnapshot
	for _, name := range []string{".git/index", "go.work", "core/go.mod", "consumer/go.mod", "consumer/go.sum"} {
		path := filepath.Join(fixture.root, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		protected = append(protected, fileSnapshot{path: path, data: data})
	}
	head, err := fixture.run(t, "", "git", "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if output, err := fixture.run(t, "", "bash", "scripts/check-candidate.sh", "consumer"); err != nil {
		t.Fatalf("current candidate: %v\n%s", err, output)
	}
	for _, file := range protected {
		data, err := os.ReadFile(file.path)
		if err != nil || !bytes.Equal(data, file.data) {
			t.Errorf("candidate changed %s: %v", file.path, err)
		}
	}
	if current, err := fixture.run(t, "", "git", "rev-parse", "HEAD"); err != nil || current != head {
		t.Fatalf("candidate changed HEAD: %q, want %q; %v", current, head, err)
	}
}

func TestCandidateReportsFailureAndChecksRemainingConsumers(t *testing.T) {
	fixture := newCandidateFixture(t)
	writeCandidateFile(t, fixture.root, "consumer/failing_test.go", `package consumer
import "testing"
func TestRejectedCandidate(t *testing.T) { t.Fatal("candidate-behavior-failed") }
`)
	output, err := fixture.run(t, "", "bash", "scripts/check-candidate.sh", "consumer", "core")
	if err == nil || !strings.Contains(output, "candidate-behavior-failed") ||
		!strings.Contains(output, "candidate checks failed: consumer") {
		t.Fatalf("candidate failure was lost: %v\n%s", err, output)
	}
	failed := strings.Index(output, "candidate-behavior-failed")
	remaining := strings.Index(output, ": core\n")
	if remaining <= failed || !strings.Contains(output[remaining:], "ok  \tgithub.com/Tangerg/scope/core") {
		t.Fatalf("remaining consumer did not run after failure:\n%s", output)
	}
}

func newCandidateFixture(t *testing.T) checkFixture {
	t.Helper()
	fixture := newCheckFixture(t, checkPseudoVersion)
	root := repositoryRoot(t)
	for _, name := range []string{"scripts/check-candidate.sh", "scripts/candidateproxy.go", "dev/repoarch/go.mod", "dev/repoarch/go.sum"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		writeCandidateFile(t, fixture.root, name, string(data))
	}
	writeCandidateFile(t, fixture.root, ".gitignore", "/modcache/\n/proxy/\n/bin/\n/consumer/ignored_test.go\n")
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	cache, err := exec.CommandContext(t.Context(), goPath, "env", "GOMODCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	// Bootstrap uses dependencies already loaded by repoarch; consumers retain the isolated proxy and cache.
	writeCandidateFile(t, fixture.root, "bin/go", `#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == build ]]; then
  export GOMODCACHE="$CANDIDATE_BOOTSTRAP_CACHE" GOPROXY=off
fi
exec "$CANDIDATE_REAL_GO" "$@"
`)
	fixture.env = append(fixture.env,
		"PATH="+filepath.Join(fixture.root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"CANDIDATE_REAL_GO="+goPath,
		"CANDIDATE_BOOTSTRAP_CACHE="+strings.TrimSpace(string(cache)),
		"GOFLAGS="+os.Getenv("GOFLAGS")+" -modcacherw",
	)
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "."},
		{"-c", "user.name=Scope candidate fixture", "-c", "user.email=fixture@example.test", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "candidate fixture"},
	} {
		if output, err := fixture.run(t, "", append([]string{"git"}, args...)...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	return fixture
}

func writeCandidateFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}
