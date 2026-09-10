package workflow

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkflowExcludesHostStateAndSecondRuntimeAbstractions(t *testing.T) {
	forbiddenNames := map[string]bool{
		"Store": true, "Journal": true, "Registry": true, "Graph": true,
		"Scheduler": true, "Repository": true, "Transaction": true, "Lease": true,
	}
	forbiddenFragments := []string{"Session", "Conversation", "Workspace", "WriteSet"}
	for _, path := range workflowProductionGoFiles(t) {
		file := parseWorkflowFile(t, path)
		ast.Inspect(file, func(node ast.Node) bool {
			for _, name := range workflowDeclaredNames(node) {
				forbidden := forbiddenNames[name.Name]
				for _, fragment := range forbiddenFragments {
					forbidden = forbidden || strings.Contains(name.Name, fragment)
				}
				if forbidden {
					t.Errorf("%s declares forbidden abstraction %q", path, name.Name)
				}
			}
			return true
		})
	}
}

func TestWorkflowStageAlgebraRemainsSealed(t *testing.T) {
	for _, path := range workflowProductionGoFiles(t) {
		file := parseWorkflowFile(t, path)
		for _, declaration := range file.Decls {
			assertWorkflowDeclarationSealed(t, path, declaration)
		}
	}
}

func workflowProductionGoFiles(t *testing.T) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func parseWorkflowFile(t *testing.T, path string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func assertWorkflowDeclarationSealed(t *testing.T, path string, declaration ast.Decl) {
	t.Helper()
	general, ok := declaration.(*ast.GenDecl)
	if !ok {
		return
	}
	for _, specification := range general.Specs {
		typeSpec, ok := specification.(*ast.TypeSpec)
		if !ok {
			continue
		}
		if typeSpec.Name.IsExported() {
			if _, open := typeSpec.Type.(*ast.InterfaceType); open {
				t.Errorf("%s exposes open Workflow behavior interface %s", path, typeSpec.Name)
			}
		}
		if typeSpec.Name.Name == "Stage" {
			assertStageHasNoExportedFields(t, path, typeSpec)
		}
	}
}

func assertStageHasNoExportedFields(t *testing.T, path string, specification *ast.TypeSpec) {
	t.Helper()
	structure, ok := specification.Type.(*ast.StructType)
	if !ok {
		t.Errorf("%s declares Stage as a non-struct", path)
		return
	}
	for _, field := range structure.Fields.List {
		for _, name := range field.Names {
			if name.IsExported() {
				t.Errorf("%s exposes mutable Stage field %s", path, name)
			}
		}
	}
}

func workflowDeclaredNames(node ast.Node) []*ast.Ident {
	switch declaration := node.(type) {
	case *ast.TypeSpec:
		return []*ast.Ident{declaration.Name}
	case *ast.FuncDecl:
		return []*ast.Ident{declaration.Name}
	case *ast.ValueSpec:
		return declaration.Names
	case *ast.Field:
		return declaration.Names
	default:
		return nil
	}
}
