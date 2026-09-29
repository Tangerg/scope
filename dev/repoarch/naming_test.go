package repoarch

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const goListPackageNameFormat = "{{.ImportPath}}\t{{.Name}}"

func TestReceiversAreTheirTypeInitial(t *testing.T) {
	t.Parallel()
	forEachGoFile(t, func(path string, fset *token.FileSet, file *ast.File) {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv == nil || len(function.Recv.List) != 1 {
				continue
			}
			names := function.Recv.List[0].Names
			if len(names) != 1 || names[0].Name == "_" {
				continue
			}
			typeName := receiverTypeName(function.Recv.List[0].Type)
			if typeName == "" {
				continue
			}
			want := strings.ToLower(typeName[:1])
			if names[0].Name != want {
				t.Errorf("%s:%d %s.%s uses receiver %q; want %q, the type initial",
					path, fset.Position(function.Pos()).Line,
					typeName, function.Name.Name, names[0].Name, want)
			}
		}
	})
}

func TestParametersDoNotShadowImportedPackages(t *testing.T) {
	t.Parallel()
	packageNames := loadPackageNames(t)
	forEachGoFile(t, func(path string, fset *token.FileSet, file *ast.File) {
		imported := importedPackageNames(t, path, file, packageNames)
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Type.Params == nil {
				continue
			}
			for _, parameter := range function.Type.Params.List {
				for _, name := range parameter.Names {
					if !imported[name.Name] {
						continue
					}
					t.Errorf("%s:%d parameter %q of %s shadows the imported package of the same name",
						path, fset.Position(name.Pos()).Line, name.Name, function.Name.Name)
				}
			}
		}
	})
}

func TestImportedPackageNamesUseResolvedPackageIdentity(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "identity.go", `package identity
import (
	"math/rand/v2"
	sdk "example.com/client/v3"
	_ "example.com/sideeffect/v4"
)`, 0)
	if err != nil {
		t.Fatal(err)
	}

	got := importedPackageNames(t, "identity.go", file, map[string]string{
		"math/rand/v2":              "rand",
		"example.com/client/v3":     "client",
		"example.com/sideeffect/v4": "sideeffect",
	})
	want := map[string]bool{"rand": true, "sdk": true}
	if !maps.Equal(got, want) {
		t.Fatalf("imported package names = %v, want %v", got, want)
	}
}

// Guards share one workspace go list because it takes seconds.
var workspacePackageNames struct {
	once  sync.Once
	names map[string]string
	err   error
}

func loadPackageNames(t *testing.T) map[string]string {
	t.Helper()
	workspacePackageNames.once.Do(func() {
		root := repositoryRoot(t)
		workspacePackageNames.names, workspacePackageNames.err = listPackageNames(root, discoverModules(t, root))
	})
	if workspacePackageNames.err != nil {
		t.Fatal(workspacePackageNames.err)
	}
	return workspacePackageNames.names
}

func listPackageNames(root string, modules map[string]repositoryModule) (map[string]string, error) {
	patterns := make([]string, 0, len(modules))
	for _, module := range modules {
		patterns = append(patterns, "./"+module.dir+"/...")
	}
	slices.Sort(patterns)

	arguments := []string{"list", "-deps", "-test", "-f", goListPackageNameFormat}
	command := exec.Command("go", append(arguments, patterns...)...)
	command.Dir = root
	// The guard may run as an isolated module, but package identities belong
	// to the repository workspace being inspected.
	command.Env = append(command.Environ(), "GOWORK="+filepath.Join(root, "go.work"))
	var diagnostics bytes.Buffer
	command.Stderr = &diagnostics
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("resolve Go package names: %w\n%s", err, diagnostics.Bytes())
	}

	names := make(map[string]string)
	for line := range strings.Lines(string(output)) {
		line = strings.TrimSuffix(line, "\n")
		importPath, name, ok := strings.Cut(line, "\t")
		if !ok || importPath == "" || name == "" {
			return nil, fmt.Errorf("go list returned malformed package identity %q", line)
		}
		names[importPath] = name
	}
	return names, nil
}

func importedPackageNames(
	t *testing.T,
	sourcePath string,
	file *ast.File,
	packageNames map[string]string,
) map[string]bool {
	t.Helper()
	names := make(map[string]bool, len(file.Imports))
	for _, specification := range file.Imports {
		if name := importedName(t, sourcePath, specification, packageNames); name != "" {
			names[name] = true
		}
	}
	return names
}

// importedName returns the identifier an import binds in its file, or "" for
// blank and dot imports, which bind none.
func importedName(t *testing.T, sourcePath string, specification *ast.ImportSpec, packageNames map[string]string) string {
	t.Helper()
	if specification.Name != nil {
		if name := specification.Name.Name; name != "_" && name != "." {
			return name
		}
		return ""
	}
	importPath := importPathOf(t, sourcePath, specification)
	name, ok := packageNames[importPath]
	if !ok {
		t.Fatalf("%s imports %q, whose package name go list did not report", sourcePath, importPath)
	}
	return name
}

func importNamesWhere(t *testing.T, sourcePath string, file *ast.File, include func(importPath string) bool) map[string]struct{} {
	t.Helper()
	packageNames := loadPackageNames(t)
	names := make(map[string]struct{})
	for _, specification := range file.Imports {
		if !include(importPathOf(t, sourcePath, specification)) {
			continue
		}
		if name := importedName(t, sourcePath, specification, packageNames); name != "" {
			names[name] = struct{}{}
		}
	}
	return names
}

func importPathOf(t *testing.T, sourcePath string, specification *ast.ImportSpec) string {
	t.Helper()
	importPath, err := strconv.Unquote(specification.Path.Value)
	if err != nil {
		t.Fatalf("%s has invalid import path %s: %v", sourcePath, specification.Path.Value, err)
	}
	return importPath
}

func receiverTypeName(expr ast.Expr) string {
	switch typed := expr.(type) {
	case *ast.StarExpr:
		return receiverTypeName(typed.X)
	case *ast.Ident:
		return typed.Name
	case *ast.IndexExpr:
		return receiverTypeName(typed.X)
	case *ast.IndexListExpr:
		return receiverTypeName(typed.X)
	}
	return ""
}

func forEachGoFile(t *testing.T, visit func(path string, fset *token.FileSet, file *ast.File)) {
	t.Helper()
	walkGoFiles(t, repositoryRoot(t), false, visit)
}

func walkProductionGoFiles(t *testing.T, root string, visit func(path string, fset *token.FileSet, file *ast.File)) {
	t.Helper()
	walkGoFiles(t, root, true, visit)
}

// A file that fails to parse fails the guard instead of escaping it.
func walkGoFiles(t *testing.T, root string, productionOnly bool, visit func(path string, fset *token.FileSet, file *ast.File)) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			if shouldSkipRepositoryDir(relative, entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || productionOnly && strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		visit(relative, fset, file)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
