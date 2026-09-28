package repoarch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestClosedSchemaVocabulariesUseNamedTypes(t *testing.T) {
	t.Parallel()
	walkProductionGoFiles(t, func(path string, fset *token.FileSet, file *ast.File) {
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
	walkProductionGoFiles(t, func(path string, fset *token.FileSet, file *ast.File) {
		for _, declaration := range file.Decls {
			method, ok := declaration.(*ast.FuncDecl)
			if !ok || method.Name.Name != "Validate" || method.Recv == nil || method.Body == nil ||
				len(method.Recv.List) != 1 || len(method.Recv.List[0].Names) != 1 {
				continue
			}
			receiver := method.Recv.List[0].Names[0].Name
			ast.Inspect(method.Body, func(node ast.Node) bool {
				var mutated bool
				switch statement := node.(type) {
				case *ast.AssignStmt:
					for _, target := range statement.Lhs {
						mutated = mutated || expressionUsesReceiver(target, receiver)
					}
				case *ast.IncDecStmt:
					mutated = expressionUsesReceiver(statement.X, receiver)
				case *ast.CallExpr:
					identifier, builtin := statement.Fun.(*ast.Ident)
					if builtin && (identifier.Name == "clear" || identifier.Name == "delete") && len(statement.Args) > 0 {
						mutated = expressionUsesReceiver(statement.Args[0], receiver)
					}
				}
				if mutated {
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

func walkProductionGoFiles(
	t *testing.T,
	visit func(path string, fset *token.FileSet, file *ast.File),
) {
	t.Helper()
	root := repositoryRoot(t)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			if shouldSkipRepositoryDir(relative, entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") ||
			strings.HasSuffix(path, ".generated.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		visit(path, fset, file)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
