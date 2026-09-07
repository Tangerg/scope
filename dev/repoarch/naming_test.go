package repoarch

import (
	"bytes"
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
	"testing"
)

const goListPackageNameFormat = "{{.ImportPath}}\t{{.Name}}"

// TestReceiversAreTheirTypeInitial keeps one receiver spelling across the
// repository. A receiver named after a word inside the type reads fine in
// isolation but makes two methods on sibling types look unrelated, and a
// receiver spelled out in full collides with the domain vocabulary used for
// locals in the same body.
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

// TestParametersDoNotShadowImportedPackages keeps an identifier from meaning
// two things inside one body. A parameter named like an imported package makes
// `filter.Predicate` and `filter.Accept` read alike while resolving to
// different objects, which survives review and misleads the next reader.
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

func loadPackageNames(t *testing.T) map[string]string {
	t.Helper()
	root := repositoryRoot(t)
	modules := discoverModules(t, root)
	patterns := make([]string, 0, len(modules))
	for _, module := range modules {
		patterns = append(patterns, "./"+module.dir+"/...")
	}
	slices.Sort(patterns)

	arguments := []string{"list", "-deps", "-test", "-f", goListPackageNameFormat}
	command := exec.CommandContext(t.Context(), "go", append(arguments, patterns...)...)
	command.Dir = root
	// The guard may run as an isolated module, but package identities belong
	// to the repository workspace being inspected.
	command.Env = append(command.Environ(), "GOWORK="+filepath.Join(root, "go.work"))
	// Dependency download diagnostics on stderr are not package identities.
	var diagnostics bytes.Buffer
	command.Stderr = &diagnostics
	output, err := command.Output()
	if err != nil {
		t.Fatalf("resolve Go package names: %v\n%s", err, diagnostics.Bytes())
	}

	names := make(map[string]string)
	for line := range strings.Lines(string(output)) {
		line = strings.TrimSuffix(line, "\n")
		importPath, name, ok := strings.Cut(line, "\t")
		if !ok || importPath == "" || name == "" {
			t.Fatalf("go list returned malformed package identity %q", line)
		}
		names[importPath] = name
	}
	return names
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
		if specification.Name != nil {
			name := specification.Name.Name
			if name != "_" && name != "." {
				names[name] = true
			}
			continue
		}

		importPath, err := strconv.Unquote(specification.Path.Value)
		if err != nil {
			t.Fatalf("%s has invalid import path %s: %v", sourcePath, specification.Path.Value, err)
		}
		name, ok := packageNames[importPath]
		if !ok {
			t.Fatalf("%s imports %q, whose package name go list did not report", sourcePath, importPath)
		}
		names[name] = true
	}
	return names
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

// forEachGoFile visits every Go source file in the repository, including tests,
// because a naming rule that stops at production code drifts in the tests that
// document it.
func forEachGoFile(t *testing.T, visit func(path string, fset *token.FileSet, file *ast.File)) {
	t.Helper()
	root := repositoryRoot(t)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path == root {
				return nil
			}
			name := entry.Name()
			if name == "node_modules" || name == "testdata" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		visit(filepath.ToSlash(relative), fset, file)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
