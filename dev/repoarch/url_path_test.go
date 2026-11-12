package repoarch

import (
	"go/ast"
	"go/token"
	"strconv"
	"testing"
)

// Escaping alone leaves "." and ".." literal, and URL resolution then drops or
// climbs a segment, so every interpolated path segment has one encoder.
func TestURLPathSegmentsHaveOneEncoder(t *testing.T) {
	const owner = "core/httpclient/path_segment.go"
	walkProductionGoFiles(t, repositoryRoot(t), func(path string, fset *token.FileSet, file *ast.File) {
		if path == owner {
			return
		}
		for _, imported := range file.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if importPath != "net/url" {
				continue
			}
			name := "url"
			if imported.Name != nil {
				name = imported.Name.Name
			}
			ast.Inspect(file, func(node ast.Node) bool {
				selector, ok := node.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "PathEscape" {
					return true
				}
				if qualifier, ok := selector.X.(*ast.Ident); ok && qualifier.Name == name {
					t.Errorf("%s:%d: encode path segments with httpclient.PathSegment", path, fset.Position(node.Pos()).Line)
				}
				return true
			})
		}
	})
}
