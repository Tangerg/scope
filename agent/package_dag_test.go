package agent

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
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != "." && excludedArchitectureDirectory(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		packagePath := filepath.ToSlash(filepath.Dir(path))
		if actualDependencies[packagePath] == nil {
			actualDependencies[packagePath] = make(map[string]struct{})
		}
		file, err := parser.ParseFile(files, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
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
	case isInteractionDependency(importPath) && !isPackageOrChild(packagePath, "strategy/interaction"):
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
// require an inventory exception. Concrete Strategies compose through Agent.
func allowedAgentDependency(source, dependency string) bool {
	if source == "." {
		return false
	}
	if dependency == "." {
		return true
	}
	if isPackageOrChild(source, "internal/conformancetest") {
		return isPackageOrChild(dependency, "agenttest") || isPackageOrChild(dependency, "internal/conformancetest")
	}
	if isPackageOrChild(source, "strategy/internal") {
		return isPackageOrChild(dependency, "strategy/internal")
	}
	if strings.HasPrefix(source, "strategy/") {
		owner, _, _ := strings.Cut(strings.TrimPrefix(source, "strategy/"), "/")
		return isPackageOrChild(dependency, "strategy/"+owner) || isPackageOrChild(dependency, "strategy/internal")
	}
	owner, _, _ := strings.Cut(source, "/")
	return isPackageOrChild(dependency, owner)
}

func TestAgentDependencyOwnershipRules(t *testing.T) {
	for _, test := range []struct {
		source     string
		dependency string
		allowed    bool
	}{
		{".", "strategy/workflow", false},
		{".", "strategy/internal/childcall", false},
		{"strategy/workflow", ".", true},
		{"strategy/workflow", "strategy/internal/newprotocol", true},
		{"strategy/internal/newprotocol", ".", true},
		{"strategy/internal/newprotocol", "strategy/workflow", false},
		{"strategy/workflow", "strategy/planning", false},
		{"strategy/planning/goap", "strategy/planning", true},
		{"strategy/interaction", "agenttest", false},
		{"messaging", "strategy/collaboration", false},
		{"messaging", "messaging/internal/codec", true},
		{"internal/conformancetest", "agenttest", true},
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
