package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"testing"
)

// Repository-wide receiver, locality, and import-name rules live in dev/repoarch.
// These guards cover the declaration hazards that recur inside Agent.
func TestAgentDeclarationsKeepOneMeaning(t *testing.T) {
	marker := regexp.MustCompile(`(?m)^\s*(?://|/\*|\*)\s*(TODO|FIXME|XXX|HACK)\b`)
	for _, path := range frameworkProductionGoFiles(t) {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		for _, comments := range file.Comments {
			for _, comment := range comments.List {
				if marker.MatchString(comment.Text) {
					t.Errorf("%s: unfinished work must not replace implementation", fset.Position(comment.Pos()))
				}
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.GenDecl:
				if node.Tok == token.CONST && mixesIotaWithUnrelatedConstants(node) {
					t.Errorf("%s: iota vocabulary and unrelated constants need separate declarations", fset.Position(node.Pos()))
				}
			case *ast.RangeStmt:
				if node.Tok != token.DEFINE {
					break
				}
				for _, statement := range node.Body.List {
					assignment, ok := statement.(*ast.AssignStmt)
					if !ok || assignment.Tok != token.DEFINE || len(assignment.Lhs) != len(assignment.Rhs) {
						continue
					}
					for index, left := range assignment.Lhs {
						name, named := left.(*ast.Ident)
						right, copied := assignment.Rhs[index].(*ast.Ident)
						if named && copied && name.Name == right.Name &&
							(isNamedIdentifier(node.Key, name.Name) || isNamedIdentifier(node.Value, name.Name)) {
							t.Errorf("%s: range variables are already iteration-scoped", fset.Position(assignment.Pos()))
						}
					}
				}
			}
			return true
		})
	}
}

func isNamedIdentifier(expression ast.Expr, name string) bool {
	identifier, ok := expression.(*ast.Ident)
	return ok && identifier.Name == name
}

func mixesIotaWithUnrelatedConstants(group *ast.GenDecl) bool {
	var usesIota bool
	ast.Inspect(group, func(node ast.Node) bool {
		if identifier, ok := node.(*ast.Ident); ok && identifier.Name == "iota" {
			usesIota = true
		}
		return true
	})
	if !usesIota {
		return false
	}
	var enumType string
	for index, specification := range group.Specs {
		spec := specification.(*ast.ValueSpec)
		if len(spec.Values) == 0 {
			continue
		}
		var name string
		if typ, ok := spec.Type.(*ast.Ident); ok {
			name = typ.Name
		}
		if index == 0 {
			enumType = name
		}
		if name != enumType {
			return true
		}
		if name == "" {
			var memberUsesIota bool
			ast.Inspect(spec, func(node ast.Node) bool {
				if identifier, ok := node.(*ast.Ident); ok && identifier.Name == "iota" {
					memberUsesIota = true
				}
				return true
			})
			if !memberUsesIota {
				return true
			}
		}
	}
	return false
}
