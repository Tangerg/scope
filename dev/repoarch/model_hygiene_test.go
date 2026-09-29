package repoarch

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestClosedSchemaVocabulariesUseNamedTypes(t *testing.T) {
	t.Parallel()
	walkProductionGoFiles(t, repositoryRoot(t), func(path string, fset *token.FileSet, file *ast.File) {
		ast.Inspect(file, func(node ast.Node) bool {
			field, ok := node.(*ast.Field)
			if !ok || field.Tag == nil || !schemaTagDeclaresEnum(field.Tag.Value) {
				return true
			}
			identifier, rawString := field.Type.(*ast.Ident)
			if !rawString || identifier.Name != "string" {
				return true
			}
			position := fset.Position(field.Pos())
			t.Errorf(
				"%s:%d closed jsonschema enum uses raw string; declare a named string type that owns the vocabulary",
				filepath.ToSlash(path), position.Line,
			)
			return true
		})
	})
}

func TestValidateMethodsAreSideEffectFree(t *testing.T) {
	t.Parallel()
	walkProductionGoFiles(t, repositoryRoot(t), func(path string, fset *token.FileSet, file *ast.File) {
		for _, declaration := range file.Decls {
			method, ok := declaration.(*ast.FuncDecl)
			if !ok || method.Name.Name != "Validate" || method.Recv == nil || method.Body == nil ||
				len(method.Recv.List) != 1 || len(method.Recv.List[0].Names) != 1 {
				continue
			}
			receiver := method.Recv.List[0].Names[0].Name
			ast.Inspect(method.Body, func(node ast.Node) bool {
				if mutatesReceiver(node, receiver) {
					position := fset.Position(node.Pos())
					t.Errorf(
						"%s:%d Validate mutates receiver %s; normalization must be an explicit copy-producing operation",
						filepath.ToSlash(path), position.Line, receiver,
					)
					return false
				}
				return true
			})
		}
	})
}

func mutatesReceiver(node ast.Node, receiver string) bool {
	switch statement := node.(type) {
	case *ast.AssignStmt:
		return slices.ContainsFunc(statement.Lhs, func(target ast.Expr) bool {
			return expressionUsesReceiver(target, receiver)
		})
	case *ast.IncDecStmt:
		return expressionUsesReceiver(statement.X, receiver)
	case *ast.CallExpr:
		identifier, builtin := statement.Fun.(*ast.Ident)
		return builtin && (identifier.Name == "clear" || identifier.Name == "delete") && len(statement.Args) > 0 &&
			expressionUsesReceiver(statement.Args[0], receiver)
	}
	return false
}

func schemaTagDeclaresEnum(quotedTag string) bool {
	tag, err := strconv.Unquote(quotedTag)
	if err != nil {
		return false
	}
	return strings.Contains(reflect.StructTag(tag).Get("jsonschema"), "enum=")
}

func expressionUsesReceiver(expression ast.Expr, receiver string) bool {
	for {
		switch value := expression.(type) {
		case *ast.Ident:
			return value.Name == receiver
		case *ast.SelectorExpr:
			expression = value.X
		case *ast.IndexExpr:
			expression = value.X
		case *ast.IndexListExpr:
			expression = value.X
		case *ast.ParenExpr:
			expression = value.X
		case *ast.StarExpr:
			expression = value.X
		default:
			return false
		}
	}
}
