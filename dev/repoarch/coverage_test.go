package repoarch

import (
	"fmt"
	"strings"
	"testing"
)

func newCoverageFixture(t *testing.T, profile string) scriptFailureFixture {
	t.Helper()
	fixture := newScriptFailureFixture(t, "check-coverage.sh")
	var modules []string
	for _, module := range discoverModules(t, repositoryRoot(t)) {
		modules = append(modules, module.dir)
	}
	fixture.write(t, "scripts/workspace-modules.sh", "#!/usr/bin/env bash\nprintf '%s\\n' "+strings.Join(modules, " ")+"\n")
	fixture.write(t, "etl/html/go.mod", "module github.com/Tangerg/scope/etl/html\n")
	fixture.write(t, "models/fireworks/go.mod", "module github.com/Tangerg/scope/models/fireworks\n")
	fixture.write(t, "dev/repoarch/go.mod", "module github.com/Tangerg/scope/dev/repoarch\n")
	fixture.write(t, "core/go.mod", "module github.com/Tangerg/scope/core\n")
	fixture.write(t, "profile", profile)
	fixture.write(t, "bin/go", `#!/usr/bin/env bash
printf '%s\n' "$*" >> "$SCRIPT_CALLS"
if [[ "$1" == list ]]; then echo github.com/Tangerg/scope/core; exit 0; fi
for argument in "$@"; do
  if [[ "$argument" == -coverprofile=* && "${SCRIPT_SKIP_PROFILE:-0}" != 1 ]]; then
    cat "$SCRIPT_ROOT/profile" > "${argument#-coverprofile=}"
  fi
done
if [[ "${SCRIPT_TEST_FAILURE:-0}" == 1 ]]; then echo test-execution-failed >&2; exit 23; fi
`)
	return fixture
}

func TestCoverageUsesExactWholeModuleStatementCounts(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		covered int
		total   int
		pass    bool
	}{
		{"rounded to ninety is below ninety", 8996, 10000, false},
		{"exactly ninety", 9, 10, true},
		{"above ninety", 91, 100, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile := fmt.Sprintf("mode: set\ngithub.com/Tangerg/scope/etl/html/reader.go:1.1,2.1 %d 1\ngithub.com/Tangerg/scope/etl/html/reader.go:3.1,4.1 %d 0\n", test.covered, test.total-test.covered)
			fixture := newCoverageFixture(t, profile)
			output, err := fixture.run(t, "check-coverage.sh", "etl/html")
			if (err == nil) != test.pass || strings.Contains(output, "coverage budget passed") != test.pass {
				t.Fatalf("coverage = %d/%d: %v\n%s", test.covered, test.total, err, output)
			}
			if !strings.Contains(fixture.calls(t), "test -count=1 -coverpkg=./... -coverprofile=") || !strings.HasSuffix(strings.TrimSpace(fixture.calls(t)), " ./...") {
				t.Fatalf("coverage omitted module consumers or owners: %q", fixture.calls(t))
			}
		})
	}
}

func TestCoveragePreservesExactPriorRatioWithoutRounding(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		covered int
		total   int
		pass    bool
	}{
		{13, 14, true},
		{26, 28, true},
		{25999, 28000, false},
	} {
		t.Run(fmt.Sprintf("%d-of-%d", test.covered, test.total), func(t *testing.T) {
			profile := fmt.Sprintf("mode: set\ngithub.com/Tangerg/scope/models/fireworks/chat.go:1.1,2.1 %d 1\ngithub.com/Tangerg/scope/models/fireworks/chat.go:3.1,4.1 %d 0\n", test.covered, test.total-test.covered)
			fixture := newCoverageFixture(t, profile)
			output, err := fixture.run(t, "check-coverage.sh", "models/fireworks")
			if (err == nil) != test.pass || strings.Contains(output, "coverage budget passed") != test.pass {
				t.Fatalf("coverage = %d/%d: %v\n%s", test.covered, test.total, err, output)
			}
		})
	}
}

func TestCoverageRejectsMissingAndMalformedEvidence(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		profile string
	}{
		{"empty", ""},
		{"header", "coverage: 100%\n"},
		{"row", "mode: set\nunreadable\n"},
		{"count", "mode: set\ngithub.com/Tangerg/scope/etl/html/reader.go:1.1,2.1 10 nope\n"},
		{"foreign owner", "mode: set\ngithub.com/Tangerg/scope/core/value.go:1.1,2.1 10 1\n"},
		{"conflicting block", "mode: set\ngithub.com/Tangerg/scope/etl/html/reader.go:1.1,2.1 10 1\ngithub.com/Tangerg/scope/etl/html/reader.go:1.1,2.1 20 1\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCoverageFixture(t, test.profile)
			output, err := fixture.run(t, "check-coverage.sh", "etl/html")
			if err == nil || !strings.Contains(output, "unreadable coverage profile") || strings.Contains(output, "coverage budget passed") {
				t.Fatalf("invalid profile became success: %v\n%s", err, output)
			}
		})
	}
}

func TestCoverageCountsRepeatedBlocksOnceAcrossConsumers(t *testing.T) {
	t.Parallel()
	fixture := newCoverageFixture(t, "mode: set\ngithub.com/Tangerg/scope/etl/html/reader.go:1.1,2.1 9 0\ngithub.com/Tangerg/scope/etl/html/reader.go:1.1,2.1 9 1\ngithub.com/Tangerg/scope/etl/html/reader.go:3.1,4.1 1 0\n")
	output, err := fixture.run(t, "check-coverage.sh", "etl/html")
	if err != nil || !strings.Contains(output, "9/10;") {
		t.Fatalf("one source block gained multiple owners: %v\n%s", err, output)
	}
}

func TestCoveragePreservesExecutionAndInventoryFailures(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		target string
		env    string
		want   string
	}{
		{"failed tests", "etl/html", "SCRIPT_TEST_FAILURE=1", "test-execution-failed"},
		{"missing profile", "etl/html", "SCRIPT_SKIP_PROFILE=1", "unreadable coverage profile"},
		{"unknown module", "missing", "", "unknown coverage module"},
		{"changed package inventory", "core", "", "core coverage package inventory is stale"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCoverageFixture(t, "mode: set\ngithub.com/Tangerg/scope/etl/html/reader.go:1.1,2.1 10 1\n")
			if test.env != "" {
				fixture.env = append(fixture.env, test.env)
			}
			output, err := fixture.run(t, "check-coverage.sh", test.target)
			if err == nil || !strings.Contains(output, test.want) || strings.Contains(output, "coverage budget passed") {
				t.Fatalf("failed boundary became success: %v\n%s", err, output)
			}
		})
	}
}

func TestCoverageNoStatementsBudgetRejectsNewExecutableCode(t *testing.T) {
	t.Parallel()
	for _, statements := range []bool{false, true} {
		t.Run(fmt.Sprint(statements), func(t *testing.T) {
			profile := "mode: set\n"
			if statements {
				profile += "github.com/Tangerg/scope/dev/repoarch/value.go:1.1,2.1 1 1\n"
			}
			fixture := newCoverageFixture(t, profile)
			output, err := fixture.run(t, "check-coverage.sh", "dev/repoarch")
			if (err != nil) != statements {
				t.Fatalf("new executable code bypassed budget admission: %v\n%s", err, output)
			}
		})
	}
}
