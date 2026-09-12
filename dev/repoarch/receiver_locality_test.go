package repoarch

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"testing"
)

// Receiver behavior stays with its type so reading one owner does not require
// tracing method fragments across the package. Test helpers follow the same rule.
func TestReceiverMethodsStayWithTheirType(t *testing.T) {
	type receiverKey struct {
		directory   string
		packageName string
		typeName    string
	}
	type methodLocation struct {
		receiver receiverKey
		name     string
		file     string
		position token.Position
	}
	declarations := make(map[receiverKey]string)
	var methods []methodLocation
	forEachGoFile(t, func(path string, fset *token.FileSet, file *ast.File) {
		for _, declaration := range file.Decls {
			switch declaration := declaration.(type) {
			case *ast.GenDecl:
				if declaration.Tok != token.TYPE {
					continue
				}
				for _, specification := range declaration.Specs {
					typeSpec := specification.(*ast.TypeSpec)
					key := receiverKey{filepath.Dir(path), file.Name.Name, typeSpec.Name.Name}
					declarations[key] = path
				}
			case *ast.FuncDecl:
				if declaration.Recv == nil {
					continue
				}
				key := receiverKey{filepath.Dir(path), file.Name.Name, receiverTypeName(declaration.Recv.List[0].Type)}
				methods = append(methods, methodLocation{key, declaration.Name.Name, path, fset.Position(declaration.Pos())})
			}
		}
	})
	for _, method := range methods {
		owner, found := declarations[method.receiver]
		if !found {
			t.Errorf("%s: cannot locate receiver type %s", method.position, method.receiver.typeName)
			continue
		}
		if method.file != owner {
			t.Errorf("%s: %s.%s belongs beside its type in %s", method.position,
				method.receiver.typeName, method.name, owner)
		}
	}
}
