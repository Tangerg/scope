package repoarch

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"testing"
)

func TestVectorStoreProviderCompilersStayPrivate(t *testing.T) {
	t.Parallel()
	compilers := 0
	walkProductionGoFiles(t, filepath.Join(repositoryRoot(t), "vectorstores"), func(path string, fset *token.FileSet, file *ast.File) {
		for _, declaration := range file.Decls {
			switch value := declaration.(type) {
			case *ast.GenDecl:
				for _, specification := range value.Specs {
					typeSpec, ok := specification.(*ast.TypeSpec)
					if !ok {
						continue
					}
					switch typeSpec.Name.Name {
					case "visitor":
						compilers++
					case "Visitor":
						t.Errorf("vectorstores/%s:%d exports provider compiler type Visitor", path, fset.Position(typeSpec.Pos()).Line)
					}
				}
			case *ast.FuncDecl:
				if value.Recv == nil && value.Name.Name == "NewVisitor" {
					t.Errorf("vectorstores/%s:%d exports provider compiler constructor NewVisitor", path, fset.Position(value.Pos()).Line)
				}
				if value.Recv != nil && receiverTypeName(value.Recv.List[0].Type) == "visitor" &&
					value.Name.IsExported() && value.Name.Name != "Visit" {
					t.Errorf("vectorstores/%s:%d exports provider compiler method %s", path, fset.Position(value.Pos()).Line, value.Name.Name)
				}
			}
		}
	})
	if compilers == 0 {
		t.Fatal("found no provider filter compilers under vectorstores")
	}
}
