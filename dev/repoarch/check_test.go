package repoarch

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/mod/module"
)

func TestCheckSelectionRunsRequiredCommands(t *testing.T) {
	t.Parallel()
	script, err := os.ReadFile(filepath.Join(repositoryRoot(t), "scripts", "check.sh"))
	if err != nil {
		t.Fatal(err)
	}
	const packages = "example.test/first example.test/second"
	defaultCalls := []string{
		"workspace:go build " + packages,
		"workspace:go vet " + packages,
		"workspace:go test -count=1 " + packages,
		"workspace:go test -race -count=1 " + packages,
		"workspace:go test -tags=integration -run ^$ ./...",
		"workspace:go test -run ^$ -bench . -benchtime=1x " + packages,
		"workspace:go mod tidy -diff",
		"off:go mod tidy -diff",
		"off:go test -count=1 ./...",
		"lint",
		"coverage:consumer",
		"vuln:consumer",
	}
	for _, test := range []struct {
		name  string
		fast  string
		args  []string
		calls []string
	}{
		{name: "default", fast: "0", calls: defaultCalls},
		{
			name: "subset", fast: "0", args: []string{"isolate", "race"},
			calls: []string{
				"off:go mod tidy -diff", "off:go test -count=1 ./...",
				"workspace:go test -race -count=1 " + packages,
			},
		},
		{name: "fast", fast: "1", calls: defaultCalls[:len(defaultCalls)-1]},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			write := func(name string, data []byte) {
				t.Helper()
				path := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			write("scripts/check.sh", script)
			write("scripts/workspace-modules.sh", []byte("#!/usr/bin/env bash\necho consumer\n"))
			write("scripts/module-packages.sh", []byte("#!/usr/bin/env bash\nprintf '%s\\n' example.test/first example.test/second\n"))
			write("scripts/check-coverage.sh", []byte("#!/usr/bin/env bash\nprintf 'coverage:%s\\n' \"$1\" >> \"$CHECK_CALLS\"\n"))
			write("scripts/check-vulnerabilities.sh", []byte("#!/usr/bin/env bash\nprintf 'vuln:%s\\n' \"$1\" >> \"$CHECK_CALLS\"\n"))
			write("bin/go", []byte(`#!/usr/bin/env bash
printf '%s:go %s\n' "${GOWORK:-workspace}" "$*" >> "$CHECK_CALLS"
`))
			write("bin/golangci-lint", []byte("#!/usr/bin/env bash\necho lint >> \"$CHECK_CALLS\"\n"))
			if err := os.Mkdir(filepath.Join(root, "consumer"), 0o700); err != nil {
				t.Fatal(err)
			}
			callsPath := filepath.Join(root, "calls")
			fixture := checkFixture{
				root: root,
				env: append(os.Environ(), "PATH="+filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
					"GOWORK=", "MODULE=", "FAST="+test.fast, "CHECK_CALLS="+callsPath),
			}
			args := append([]string{"bash", "scripts/check.sh"}, test.args...)
			if output, err := fixture.run(t, "", args...); err != nil {
				t.Fatalf("run checks: %v\n%s", err, output)
			}
			data, err := os.ReadFile(callsPath)
			if err != nil {
				t.Fatal(err)
			}
			calls := strings.Split(strings.TrimSpace(string(data)), "\n")
			if !slices.Equal(calls, test.calls) {
				t.Fatalf("commands = %q, want %q", calls, test.calls)
			}
		})
	}
}

// CI names no checks so that the default set in scripts/check.sh is the only
// list that can advance; a copied list drifts from it.
func TestCIDoesNotRestateTheCheckSet(t *testing.T) {
	t.Parallel()
	workflow, err := os.ReadFile(filepath.Join(repositoryRoot(t), ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	invoked := false
	for line := range strings.Lines(string(workflow)) {
		_, selection, invokes := strings.Cut(line, "scripts/check.sh")
		if !invokes {
			continue
		}
		invoked = true
		if strings.TrimSpace(selection) != "" {
			t.Errorf("CI selects checks itself: %s", strings.TrimSpace(line))
		}
	}
	if !invoked {
		t.Error("CI no longer runs scripts/check.sh")
	}
	for _, script := range []string{"check-coverage.sh"} {
		if strings.Contains(string(workflow), script) {
			t.Errorf("CI restates the canonical coverage dispatch: %s", script)
		}
	}
}

func TestCoverageSelectionDelegatesToOwnerAndPreservesFailure(t *testing.T) {
	t.Parallel()
	for _, selected := range []string{
		"core", "agent", "examples", "etl/pdf", "eval", "tools", "otel/agent",
		"models/alibaba", "historystores/redis", "vectorstores/redis", "dev/repoarch",
	} {
		for _, subset := range []bool{false, true} {
			for _, fail := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/subset=%t/fail=%t", selected, subset, fail), func(t *testing.T) {
					fixture := newScriptFailureFixture(t, "check.sh")
					fixture.write(t, "scripts/workspace-modules.sh", "#!/usr/bin/env bash\nprintf '%s\\n' "+selected+"\n")
					fixture.write(t, "scripts/module-packages.sh", "#!/usr/bin/env bash\nprintf '%s\\n' example.test/first example.test/second\n")
					fixture.write(t, "scripts/check-vulnerabilities.sh", "#!/usr/bin/env bash\nexit 0\n")
					fixture.write(t, "bin/go", "#!/usr/bin/env bash\nexit 0\n")
					fixture.write(t, "bin/golangci-lint", "#!/usr/bin/env bash\nexit 0\n")
					fixture.write(t, selected+"/go.mod", "module example.test/consumer\n")
					fixture.write(t, "scripts/check-coverage.sh", `#!/usr/bin/env bash
printf '%s\n' "$1" >> "$SCRIPT_CALLS"
if [[ "$SCRIPT_COVERAGE_FAILURE" == 1 ]]; then echo coverage-budget-failed >&2; exit 23; fi
`)
					failure := "0"
					if fail {
						failure = "1"
					}
					fixture.env = append(fixture.env, "MODULE="+selected, "FAST=0", "SCRIPT_COVERAGE_FAILURE="+failure)
					var args []string
					if subset {
						args = []string{"coverage"}
					}
					output, err := fixture.run(t, "check.sh", args...)
					if fixture.calls(t) != selected+"\n" || (err != nil) != fail {
						t.Fatalf("coverage selection=%q error=%v output=%s", fixture.calls(t), err, output)
					}
					if fail && (!strings.Contains(output, selected+"/coverage") || strings.Contains(output, "all green")) {
						t.Fatalf("coverage failure became success: %s", output)
					}
				})
			}
		}
	}
}

func TestIsolationDetectsDependencySemanticDrift(t *testing.T) {
	t.Parallel()
	for _, version := range []string{checkPseudoVersion, "v0.16.1"} {
		t.Run(version, func(t *testing.T) {
			fixture := newCheckFixture(t, version)
			if output, err := fixture.run(t, "", "bash", "scripts/check.sh", "test"); err != nil {
				t.Fatalf("workspace test: %v\n%s", err, output)
			}
			if output, err := fixture.run(t, "consumer", "env", "GOWORK=off", "go", "test", "-run", "^$", "./..."); err != nil {
				t.Fatalf("isolated compilation: %v\n%s", err, output)
			}
			output, err := fixture.run(t, "", "bash", "scripts/check.sh", "isolate")
			if err == nil || !strings.Contains(output, "pinned value = 1, want 2") {
				t.Fatalf("isolation must detect dependency behavior drift: %v\n%s", err, output)
			}
			fixture.env = append(fixture.env, "MODULE=core")
			if output, err := fixture.run(t, "", "bash", "scripts/check.sh", "isolate"); err != nil {
				t.Fatalf("isolate module without Scope dependencies: %v\n%s", err, output)
			}
		})
	}
}

func TestIsolationPropagatesModuleHygieneFailure(t *testing.T) {
	t.Parallel()
	fixture := newCheckFixture(t, checkPseudoVersion)
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

const checkPseudoVersion = "v0.16.1-0.20260910000000-abcdef123456"

func newCheckFixture(t *testing.T, version string) checkFixture {
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
	dependencyMod := []byte("module " + dependency + "\n\ngo 1.27.0\n")
	write("go.work", []byte("go 1.27.0\n\nuse (\n ./core\n ./consumer\n)\n"))
	write("core/go.mod", dependencyMod)
	write("core/value.go", []byte("package core\nconst Value = 2\n"))
	write("core/value_test.go", []byte(`package core
import "testing"
func TestValue(t *testing.T) { if Value != 2 { t.Fatalf("value = %d, want 2", Value) } }
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

func TestEnumerationFailureStopsChecks(t *testing.T) {
	t.Parallel()
	for _, partial := range []bool{false, true} {
		for _, boundary := range []string{"workspace", "packages", "buildable"} {
			t.Run(fmt.Sprintf("%s/partial=%t", boundary, partial), func(t *testing.T) {
				fixture := newCheckFixture(t, checkPseudoVersion)
				name := "module-packages.sh"
				body := "#!/usr/bin/env bash\n"
				if boundary == "workspace" {
					name = "workspace-modules.sh"
				}
				if boundary == "buildable" {
					body += "if [[ $1 != --buildable ]]; then echo github.com/Tangerg/scope/consumer; exit 0; fi\n"
				}
				if partial {
					if boundary == "workspace" {
						body += "echo consumer\n"
					} else {
						body += "echo github.com/Tangerg/scope/consumer\n"
					}
				}
				body += "echo enumeration-failed >&2\nexit 23\n"
				if err := os.WriteFile(filepath.Join(fixture.root, "scripts", name), []byte(body), 0o700); err != nil {
					t.Fatal(err)
				}
				output, err := fixture.run(t, "", "bash", "scripts/check.sh", "build")
				if err == nil || !strings.Contains(output, "enumeration-failed") || strings.Contains(output, "all green") {
					t.Fatalf("enumeration failure must stop checks: %v\n%s", err, output)
				}
			})
		}
	}
}

func TestModulePackagesPropagatesGoListFailure(t *testing.T) {
	t.Parallel()
	fixture := newCheckFixture(t, checkPseudoVersion)
	if err := os.WriteFile(filepath.Join(fixture.root, "consumer", "broken.go"), []byte("package 123broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"consumer"}, {"--buildable", "consumer"}} {
		output, err := fixture.run(t, "", append([]string{"bash", "scripts/module-packages.sh"}, args...)...)
		if err == nil || !strings.Contains(output, "expected 'IDENT'") {
			t.Fatalf("go list failure must propagate: %v\n%s", err, output)
		}
	}
}
