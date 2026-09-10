package repoarch

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/module"
)

func TestPinnedTestsDetectDependencySemanticDrift(t *testing.T) {
	t.Parallel()
	fixture := newCheckFixture(t)
	for _, check := range []string{"test", "isolate"} {
		if output, err := fixture.run(t, "", "bash", "scripts/check.sh", check); err != nil {
			t.Fatalf("%s: %v\n%s", check, err, output)
		}
	}
	output, err := fixture.run(t, "", "bash", "scripts/check.sh", "pinned-test")
	if err == nil || !strings.Contains(output, "pinned value = 1, want 2") {
		t.Fatalf("pinned dependency drift must fail the semantic test: %v\n%s", err, output)
	}

	fixture.env = append(fixture.env, "MODULE=core")
	if output, err := fixture.run(t, "", "bash", "scripts/check.sh", "pinned-test"); err != nil {
		t.Fatalf("module without a Scope pseudo-version pin must be skipped: %v\n%s", err, output)
	}
}

func TestIsolationPropagatesModuleHygieneFailure(t *testing.T) {
	t.Parallel()
	fixture := newCheckFixture(t)
	path := filepath.Join(fixture.root, "consumer", "go.mod")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte("abcdef123456"), []byte("abcdef123456 // indirect"), 1)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := fixture.run(t, "consumer", "env", "GOWORK=off", "go", "test", "-run", "^$", "./..."); err != nil {
		t.Fatalf("compilation must succeed despite the untidy requirement: %v\n%s", err, output)
	}
	if output, err := fixture.run(t, "", "bash", "scripts/check.sh", "isolate"); err == nil || !strings.Contains(output, "tidy/go.mod") {
		t.Fatalf("isolation must report the failed tidy check even when compilation succeeds: %v\n%s", err, output)
	}
}

type checkFixture struct {
	root string
	env  []string
}

func newCheckFixture(t *testing.T) checkFixture {
	t.Helper()
	root, pathErr := filepath.EvalSymlinks(t.TempDir())
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	fixture := checkFixture{root: root}
	write := func(name string, data []byte) {
		t.Helper()
		path := filepath.Join(fixture.root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"check.sh", "workspace-modules.sh", "module-packages.sh"} {
		data, err := os.ReadFile(filepath.Join(repositoryRoot(t), "scripts", name))
		if err != nil {
			t.Fatal(err)
		}
		write("scripts/"+name, data)
	}

	const dependency = "github.com/Tangerg/scope/core"
	const version = "v0.16.1-0.20260910000000-abcdef123456"
	dependencyMod := []byte("module " + dependency + "\n\ngo 1.27.0\n")
	write("go.work", []byte("go 1.27.0\n\nuse (\n ./core\n ./consumer\n)\n"))
	write("core/go.mod", dependencyMod)
	write("core/value.go", []byte("package core\nconst Value = 2\n"))
	write("core/value_test.go", []byte(`package core
import "testing"
func TestMustNotRun(t *testing.T) { t.Fatal("module has no pseudo-version pin") }
`))
	write("consumer/go.mod", []byte("module github.com/Tangerg/scope/consumer\n\ngo 1.27.0\n\nrequire "+dependency+" "+version+"\n"))
	write("consumer/value_test.go", []byte(`package consumer
import (
 "testing"
 "github.com/Tangerg/scope/core"
)
func TestDependencyValue(t *testing.T) {
 if core.Value != 2 { t.Fatalf("pinned value = %d, want 2", core.Value) }
}
`))

	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for _, source := range []struct {
		name string
		data []byte
	}{
		{"go.mod", dependencyMod},
		{"value.go", []byte("package core\nconst Value = 1\n")},
	} {
		entry, err := writer.Create(dependency + "@" + version + "/" + source.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(source.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	escaped, err := module.EscapePath(dependency)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "proxy/" + escaped + "/@v/" + version
	write(prefix+".mod", dependencyMod)
	write(prefix+".info", fmt.Appendf(nil, `{"Version":%q,"Time":"2026-09-10T00:00:00Z"}`, version))
	write(prefix+".zip", archive.Bytes())
	proxy := url.URL{Scheme: "file", Path: filepath.ToSlash(filepath.Join(fixture.root, "proxy"))}
	fixture.env = append(os.Environ(),
		"MODULE=consumer",
		"GOWORK="+filepath.Join(fixture.root, "go.work"),
		"GOMODCACHE="+filepath.Join(fixture.root, "modcache"),
		"GOPROXY="+proxy.String(),
		"GONOPROXY=none",
		"GOSUMDB=off",
		"GOTOOLCHAIN=local",
		"GOFLAGS=-modcacherw",
	)
	if output, err := fixture.run(t, "consumer", "env", "GOWORK=off", "go", "mod", "tidy"); err != nil {
		t.Fatalf("prepare isolated dependency metadata: %v\n%s", err, output)
	}
	return fixture
}

func (c checkFixture) run(t *testing.T, directory string, args ...string) (string, error) {
	t.Helper()
	command := exec.Command(args[0], args[1:]...)
	command.Dir = filepath.Join(c.root, directory)
	command.Env = c.env
	output, err := command.CombinedOutput()
	return string(output), err
}
