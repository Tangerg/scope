package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestWireDTOFieldsExcludeArbitraryRuntimeValues(t *testing.T) {
	t.Parallel()

	root := coreRoot(t)
	fset := token.NewFileSet()
	for _, filename := range productionGoFiles(t) {
		packagePath, err := filepath.Rel(root, filepath.Dir(filename))
		if err != nil {
			t.Fatal(err)
		}
		packagePath = filepath.ToSlash(packagePath)
		if _, public := targetPublicPackages[packagePath]; !public {
			continue
		}

		file, err := parser.ParseFile(fset, filename, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.TYPE {
				continue
			}
			for _, specification := range general.Specs {
				typeSpec := specification.(*ast.TypeSpec)
				structure, ok := typeSpec.Type.(*ast.StructType)
				if !ok || !ast.IsExported(typeSpec.Name.Name) || !hasJSONTag(structure) {
					continue
				}
				assertWireFieldsAreSafe(t, packagePath, typeSpec.Name.Name, structure)
			}
		}
	}
}

func TestWireDTOPointersHaveExplicitPresenceSemantics(t *testing.T) {
	t.Parallel()

	root := coreRoot(t)
	fset := token.NewFileSet()
	for _, filename := range productionGoFiles(t) {
		packagePath, err := filepath.Rel(root, filepath.Dir(filename))
		if err != nil {
			t.Fatal(err)
		}
		packagePath = filepath.ToSlash(packagePath)
		if _, public := targetPublicPackages[packagePath]; !public {
			continue
		}

		file, err := parser.ParseFile(fset, filename, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.TYPE {
				continue
			}
			for _, specification := range general.Specs {
				typeSpec := specification.(*ast.TypeSpec)
				structure, ok := typeSpec.Type.(*ast.StructType)
				if !ok || !ast.IsExported(typeSpec.Name.Name) || !hasJSONTag(structure) {
					continue
				}
				for _, field := range structure.Fields.List {
					if _, pointer := field.Type.(*ast.StarExpr); !pointer || field.Tag == nil {
						continue
					}
					tag, unquoteErr := strconv.Unquote(field.Tag.Value)
					if unquoteErr != nil {
						t.Fatal(unquoteErr)
					}
					jsonTag := reflect.StructTag(tag).Get("json")
					if hasJSONTagOption(jsonTag, "omitempty") || hasJSONTagOption(jsonTag, "omitzero") {
						continue
					}
					for _, name := range field.Names {
						if !hasRequiredPointerValidation(file, typeSpec.Name.Name, name.Name) {
							t.Errorf("%s.%s.%s is a required pointer without an explicit nil rejection in Validate", packagePath, typeSpec.Name.Name, name.Name)
						}
					}
				}
			}
		}
	}
}

func hasRequiredPointerValidation(file *ast.File, typeName, fieldName string) bool {
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "Validate" || function.Recv == nil || len(function.Recv.List) != 1 || function.Body == nil {
			continue
		}
		receiver := function.Recv.List[0]
		if receiverTypeName(receiver.Type) != typeName || len(receiver.Names) != 1 {
			continue
		}
		receiverName := receiver.Names[0].Name
		validated := false
		ast.Inspect(function.Body, func(node ast.Node) bool {
			binary, ok := node.(*ast.BinaryExpr)
			if !ok || binary.Op != token.EQL {
				return true
			}
			if requiredPointerNilComparison(binary.X, binary.Y, receiverName, fieldName) ||
				requiredPointerNilComparison(binary.Y, binary.X, receiverName, fieldName) {
				validated = true
				return false
			}
			return true
		})
		if validated {
			return true
		}
	}
	return false
}

func hasJSONTagOption(tag, option string) bool {
	parts := strings.Split(tag, ",")
	return slices.Contains(parts[1:], option)
}

func receiverTypeName(receiver ast.Expr) string {
	switch value := receiver.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.StarExpr:
		if identifier, ok := value.X.(*ast.Ident); ok {
			return identifier.Name
		}
	}
	return ""
}

func requiredPointerNilComparison(value, nilValue ast.Expr, receiverName, fieldName string) bool {
	nilIdentifier, ok := nilValue.(*ast.Ident)
	if !ok || nilIdentifier.Name != "nil" {
		return false
	}
	selector, ok := value.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != fieldName {
		return false
	}
	receiver, ok := selector.X.(*ast.Ident)
	return ok && receiver.Name == receiverName
}

func assertWireFieldsAreSafe(t *testing.T, packagePath, typeName string, structure *ast.StructType) {
	t.Helper()
	for _, field := range structure.Fields.List {
		if wireFieldIgnored(field) {
			continue
		}
		for _, name := range field.Names {
			if !ast.IsExported(name.Name) {
				continue
			}
			qualified := packagePath + "." + typeName + "." + name.Name
			if name.Name == "Params" {
				t.Errorf("%s must not reintroduce a provider parameter bag; use typed Options plus JSON-safe metadata", qualified)
			}
			if containsArbitraryRuntimeValue(field.Type) {
				t.Errorf("%s contains any/interface{}; encode provider extensions as metadata.Map at the adapter boundary", qualified)
			}
		}
	}
}

func wireFieldIgnored(field *ast.Field) bool {
	if field.Tag == nil {
		return false
	}
	tag, err := strconv.Unquote(field.Tag.Value)
	return err == nil && reflect.StructTag(tag).Get("json") == "-"
}

func hasJSONTag(structure *ast.StructType) bool {
	for _, field := range structure.Fields.List {
		if field.Tag == nil {
			continue
		}
		tag, err := strconv.Unquote(field.Tag.Value)
		if err == nil && reflect.StructTag(tag).Get("json") != "" {
			return true
		}
	}
	return false
}

func containsArbitraryRuntimeValue(expression ast.Expr) bool {
	found := false
	ast.Inspect(expression, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.Ident:
			if typed.Name == "any" {
				found = true
				return false
			}
		case *ast.InterfaceType:
			found = true
			return false
		}
		return !found
	})
	return found
}
