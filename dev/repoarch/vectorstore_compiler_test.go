package repoarch

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestVectorStoreProviderCompilersStayPrivate(t *testing.T) {
	t.Parallel()
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
}

// A native precomputed-vector provider may not quietly reintroduce the
// model calls or text entry points that its caller already owns.
func TestNativeVectorStoreHasNoEmbeddingDriver(t *testing.T) {
	root := filepath.Join(repositoryRoot(t), "vectorstores")
	walkProductionGoFiles(t, root, func(path string, _ *token.FileSet, file *ast.File) {
		for _, im := range file.Imports {
			v, err := strconv.Unquote(im.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasSuffix(v, "/core/embeddingclient") || strings.HasPrefix(v, "github.com/Tangerg/scope/models/") {
				t.Errorf("%s: native store imports encoding service %s", path, v)
			}
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Recv != nil && receiverTypeName(fn.Recv.List[0].Type) == "Store" && (fn.Name.Name == "Search" || fn.Name.Name == "Index") {
				t.Errorf("%s: native Store retains semantic operation %s", path, fn.Name.Name)
			}
		}
	})
}
