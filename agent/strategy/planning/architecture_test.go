package planning

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanningExcludesHostAbstractions(t *testing.T) {
	forbiddenNames := map[string]bool{
		"Blackboard": true, "ProcessContext": true, "ConditionEnv": true, "ConditionResolver": true,
		"Store": true, "Repository": true, "Transaction": true, "Lease": true,
	}
	forbiddenFragments := []string{"Session", "Conversation", "Workspace", "WriteSet"}
	files := token.NewFileSet()
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(files, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			for _, name := range planningDeclaredNames(node) {
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
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func planningDeclaredNames(node ast.Node) []*ast.Ident {
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
