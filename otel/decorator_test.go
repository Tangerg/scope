package otel_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// Every otel decorator has one shape: an exported Middleware method takes the
// next value and returns only the decorated value. A constructed Middleware
// cannot fail to decorate, and an unconstructed one decorates into values
// whose every call fails, so no decorator returns an error.
func TestMiddlewareDecoratorsReturnOnlyTheDecoratedValue(t *testing.T) {
	files := token.NewFileSet()
	decorators := 0
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		file, err := parser.ParseFile(files, path, nil, 0)
		if err != nil {
			return err
		}
		for _, declaration := range file.Decls {
			method, ok := declaration.(*ast.FuncDecl)
			if !ok || method.Recv == nil || !method.Name.IsExported() || receiverName(method.Recv.List[0].Type) != "Middleware" {
				continue
			}
			parameters := method.Type.Params.List
			if len(parameters) != 1 || len(parameters[0].Names) != 1 || parameters[0].Names[0].Name != "next" {
				continue
			}
			decorators++
			if results := method.Type.Results; results == nil || results.NumFields() != 1 || isError(results.List[0].Type) {
				t.Errorf("%s: Middleware.%s must return only the decorated value", files.Position(method.Pos()), method.Name.Name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if decorators < 15 {
		t.Fatalf("found %d decorators; the scan no longer sees the otel modules", decorators)
	}
}

func receiverName(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.StarExpr:
		return receiverName(value.X)
	case *ast.IndexExpr:
		return receiverName(value.X)
	case *ast.Ident:
		return value.Name
	default:
		return ""
	}
}

func isError(expression ast.Expr) bool {
	identifier, ok := expression.(*ast.Ident)
	return ok && identifier.Name == "error"
}
