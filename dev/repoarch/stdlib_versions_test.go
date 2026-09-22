package repoarch

import (
	"go/ast"
	"go/token"
	"strconv"
	"testing"
)

func TestStandardLibraryCurrentVersions(t *testing.T) {
	forEachGoFile(t, func(path string, fset *token.FileSet, file *ast.File) {
		for _, imported := range file.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if importPath == "math/rand" {
				t.Errorf("%s: use math/rand/v2", path)
			}
			if importPath != "encoding/json" {
				continue
			}
			name := "json"
			if imported.Name != nil {
				name = imported.Name.Name
			}
			if name == "." || name == "_" {
				t.Errorf("%s: encoding/json is only allowed for interoperable type declarations", path)
				continue
			}
			ast.Inspect(file, func(node ast.Node) bool {
				selector, ok := node.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				qualifier, ok := selector.X.(*ast.Ident)
				if !ok || qualifier.Name != name {
					return true
				}
				switch selector.Sel.Name {
				case "RawMessage", "Number", "Marshaler", "Unmarshaler":
				default:
					t.Errorf("%s:%d use encoding/json/v2 or encoding/json/jsontext instead of %s.%s",
						path, fset.Position(node.Pos()).Line, name, selector.Sel.Name)
				}
				return true
			})
		}
	})
}
