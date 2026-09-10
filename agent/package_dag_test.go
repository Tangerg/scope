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

var allowedPackageDependencies = map[string]map[string]struct{}{
	".":                        {},
	"agenttest":                {".": {}},
	"strategy/collaboration":   {".": {}},
	"strategy/coordination":    {".": {}},
	"messaging":                {".": {}},
	"strategy/interaction":     {".": {}},
	"strategy/planning":        {".": {}},
	"strategy/planning/goap":   {"strategy/planning": {}},
	"strategy/workflow":        {".": {}},
	"internal/conformancetest": {".": {}, "agenttest": {}},
}

func TestProductionPackageDependencyGraph(t *testing.T) {
	actualPackages := make(map[string]struct{})
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
		actualPackages[packagePath] = struct{}{}
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

	assertPackageSet(t, actualPackages)
	for packagePath, dependencies := range actualDependencies {
		allowed := allowedPackageDependencies[packagePath]
		for dependency := range dependencies {
			if _, ok := allowed[dependency]; !ok {
				t.Errorf("production package %q imports forbidden agent package %q", packagePath, dependency)
			}
		}
	}
	assertAcyclicPackageGraph(t)
}

func assertExternalPackageBoundary(t *testing.T, packagePath string, sourcePath string, importPath string) {
	t.Helper()
	switch {
	case isPackageOrChild(importPath, "github.com/Tangerg/scope/app"):
		t.Errorf("%s imports Host application package %q", sourcePath, importPath)
	case isPackageOrChild(importPath, "github.com/Tangerg/flow"):
		t.Errorf("%s imports flow instead of keeping managed Workflow execution Framework-owned: %q", sourcePath, importPath)
	case isPackageOrChild(importPath, "go.opentelemetry.io/otel") && packagePath != "otel":
		t.Errorf("%s imports OpenTelemetry outside the otel adapter: %q", sourcePath, importPath)
	case importPath == "log/slog":
		t.Errorf("%s imports a logging backend instead of publishing Framework observations", sourcePath)
	case isInteractionDependency(importPath) && packagePath != "strategy/interaction":
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

func assertPackageSet(t *testing.T, actual map[string]struct{}) {
	t.Helper()
	for packagePath := range actual {
		if _, ok := allowedPackageDependencies[packagePath]; !ok {
			t.Errorf("production package %q is not part of the accepted agent package graph", packagePath)
		}
	}
	for packagePath := range allowedPackageDependencies {
		if _, ok := actual[packagePath]; !ok {
			t.Errorf("accepted production package %q has no production Go source", packagePath)
		}
	}
}

func assertAcyclicPackageGraph(t *testing.T) {
	t.Helper()
	const (
		unvisited = iota
		visiting
		visited
	)
	states := make(map[string]int, len(allowedPackageDependencies))
	var visit func(string)
	visit = func(packagePath string) {
		switch states[packagePath] {
		case visiting:
			t.Errorf("accepted package graph contains a cycle through %q", packagePath)
			return
		case visited:
			return
		}
		states[packagePath] = visiting
		for dependency := range allowedPackageDependencies[packagePath] {
			if _, ok := allowedPackageDependencies[dependency]; !ok {
				t.Errorf("accepted package %q points to undeclared package %q", packagePath, dependency)
				continue
			}
			visit(dependency)
		}
		states[packagePath] = visited
	}
	for packagePath := range allowedPackageDependencies {
		visit(packagePath)
	}
}
