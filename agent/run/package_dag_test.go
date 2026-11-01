package run

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const moduleImportPath = "github.com/Tangerg/scope/agent"

func TestProductionPackageDependencyGraph(t *testing.T) {
	actualDependencies := make(map[string]map[string]struct{})
	files := token.NewFileSet()
	err := filepath.WalkDir("..", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != ".." && excludedArchitectureDirectory(strings.TrimPrefix(path, "../")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		packagePath, err := filepath.Rel("..", filepath.Dir(path))
		if err != nil {
			return err
		}
		packagePath = filepath.ToSlash(packagePath)
		if actualDependencies[packagePath] == nil {
			actualDependencies[packagePath] = make(map[string]struct{})
		}
		file, err := parser.ParseFile(files, path, nil, 0)
		if err != nil {
			return err
		}
		for _, violation := range agentOwnershipViolations(packagePath, file) {
			t.Errorf("%s: %s", path, violation)
		}
		for _, imported := range file.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return fmt.Errorf("decode import in %s: %w", path, err)
			}
			assertExternalPackageBoundary(t, packagePath, path, importPath)
			dependency, internal := internalPackagePath(importPath)
			if internal {
				actualDependencies[packagePath][dependency] = struct{}{}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for packagePath, dependencies := range actualDependencies {
		for dependency := range dependencies {
			if !allowedAgentDependency(packagePath, dependency) {
				t.Errorf("production package %q imports forbidden agent package %q", packagePath, dependency)
			}
		}
	}
	assertAcyclicPackageGraph(t, actualDependencies)
}

func assertExternalPackageBoundary(t *testing.T, packagePath string, sourcePath string, importPath string) {
	t.Helper()
	switch {
	case isPackageOrChild(importPath, "github.com/Tangerg/scope/app"):
		t.Errorf("%s imports Host application package %q", sourcePath, importPath)
	case isPackageOrChild(importPath, "github.com/Tangerg/flow"):
		t.Errorf("%s imports flow instead of keeping managed Workflow execution Framework-owned: %q", sourcePath, importPath)
	case isPackageOrChild(importPath, "go.opentelemetry.io/otel"):
		t.Errorf("%s imports OpenTelemetry inside Agent: %q", sourcePath, importPath)
	case importPath == "log/slog":
		t.Errorf("%s imports a logging backend instead of publishing Framework observations", sourcePath)
	case isInteractionDependency(importPath) && packagePath != ".":
		t.Errorf("%s imports Interaction-owned protocol %q outside the interaction package", sourcePath, importPath)
	}
}

func isPackageOrChild(importPath string, packagePrefix string) bool {
	return importPath == packagePrefix || strings.HasPrefix(importPath, packagePrefix+"/")
}

func isInteractionDependency(importPath string) bool {
	return isPackageOrChild(importPath, "github.com/Tangerg/scope/core/chatclient") ||
		isPackageOrChild(importPath, "github.com/Tangerg/scope/core/tool") ||
		isPackageOrChild(importPath, "github.com/Tangerg/scope/core/chat")
}

func excludedArchitectureDirectory(path string) bool {
	first, _, _ := strings.Cut(filepath.ToSlash(path), "/")
	return first == "doc" || first == "examples" || strings.HasPrefix(first, ".")
}

func internalPackagePath(importPath string) (string, bool) {
	if importPath == moduleImportPath {
		return ".", true
	}
	prefix := moduleImportPath + "/"
	if !strings.HasPrefix(importPath, prefix) {
		return "", false
	}
	return strings.TrimPrefix(importPath, prefix), true
}

// The guard follows ownership roles so a new private protocol package does not
// require an inventory exception. Strategies never import one another; the role
// composition tier is the one sanctioned exception that composes them directly.
func allowedAgentDependency(source, dependency string) bool {
	// Shared internal codecs and diagnostics are below the kernel and Strategies;
	// conformance instrumentation instead depends on the public test contracts.
	if isPackageOrChild(dependency, "internal/conformancetest") {
		return isPackageOrChild(source, "internal/conformancetest")
	}
	if isPackageOrChild(source, "internal/conformancetest") {
		return dependency == "run" || dependency == "run/runtest"
	}
	if dependency == "run" {
		return source != "run" && (!isPackageOrChild(source, "internal") || isPackageOrChild(source, "internal/strategy"))
	}
	if isPackageOrChild(dependency, "internal/strategy") {
		return isStrategyPackage(source)
	}
	if isPackageOrChild(dependency, "internal") {
		return true
	}
	if source == "run" {
		return false
	}
	if source == "." {
		return dependency == "workflow"
	}

	if source == "." || isPackageOrChild(source, "internal") {
		return false
	}
	owner, _, _ := strings.Cut(source, "/")
	return isPackageOrChild(dependency, owner) && (source != "run/runtest" || dependency == "run/runtest")

}

func isStrategyPackage(path string) bool {
	switch path {
	case ".", "team", "workflow", "planning", "coordination":
		return true
	}
	return isPackageOrChild(path, "planning/goap") || isPackageOrChild(path, "internal/strategy")
}

func TestAgentDependencyOwnershipRules(t *testing.T) {
	for _, test := range []struct {
		source     string
		dependency string
		allowed    bool
	}{
		{"run", "workflow", false}, {".", "run", true}, {"run", ".", false},
		{"run", "internal/jsonwire", true},
		{".", "internal/panicinfo", true},
		{"internal/jsonwire", "run", false},
		{"internal/jsonwire", "internal/strategy", false},
		{"internal/jsonwire", "internal/conformancetest", false},
		{"run", "internal/conformancetest", false},
		{"run", "internal/strategy/childcall", false},
		{"workflow", "run", true},
		{"workflow", "internal/strategy/newprotocol", true},
		{"internal/strategy/newprotocol", "run", true},
		{"internal/strategy/newprotocol", "workflow", false},
		{"workflow", "planning", false},
		{".", "run", true},
		{".", "workflow", true},
		{"team", "run", true},
		{".", "planning", false},
		{"planning/goap", "planning", true},
		{".", "run/runtest", false},
		{"messaging", "team", false},
		{"messaging", "messaging/internal/codec", true},
		{"internal/conformancetest", "run/runtest", true},
	} {
		if got := allowedAgentDependency(test.source, test.dependency); got != test.allowed {
			t.Errorf("dependency %s -> %s allowed = %v, want %v", test.source, test.dependency, got, test.allowed)
		}
	}
}

func assertAcyclicPackageGraph(t *testing.T, dependencies map[string]map[string]struct{}) {
	t.Helper()
	const (
		unvisited = iota
		visiting
		visited
	)
	states := make(map[string]int, len(dependencies))
	var visit func(string)
	visit = func(packagePath string) {
		switch states[packagePath] {
		case visiting:
			t.Errorf("production package graph contains a cycle through %q", packagePath)
			return
		case visited:
			return
		}
		states[packagePath] = visiting
		for dependency := range dependencies[packagePath] {
			if _, ok := dependencies[dependency]; !ok {
				t.Errorf("production package %q points to missing package %q", packagePath, dependency)
				continue
			}
			visit(dependency)
		}
		states[packagePath] = visited
	}
	for packagePath := range dependencies {
		visit(packagePath)
	}
}
