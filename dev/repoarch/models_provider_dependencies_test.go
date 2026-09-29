package repoarch

import (
	"go/ast"
	"go/token"
	"strings"
	"testing"
)

const modelsImportPrefix = repositoryModulePrefix + "/models/"

func TestProviderDependenciesAreOneWay(t *testing.T) {
	t.Parallel()

	walkProductionGoFiles(t, modelsRoot(t), func(path string, fset *token.FileSet, file *ast.File) {
		source := firstPathSegment(path)
		for _, imported := range file.Imports {
			pathValue := importPathOf(t, path, imported)
			if !strings.HasPrefix(pathValue, modelsImportPrefix) {
				continue
			}
			target := strings.TrimPrefix(pathValue, modelsImportPrefix)
			targetRoot, _, _ := strings.Cut(target, "/")
			switch {
			case source == "internal" && targetRoot != "internal" && targetRoot != "protocol":
				t.Errorf("%s:%d internal implementation must not depend on public provider %q", path, fset.Position(imported.Pos()).Line, targetRoot)
			case source != "internal" && targetRoot != "internal" && targetRoot != "protocol" && targetRoot != source:
				t.Errorf("%s:%d provider %q must not depend on peer provider %q", path, fset.Position(imported.Pos()).Line, source, targetRoot)
			}
		}
	})
}

// Exact shared protocols may be promoted by alias; provider-private
// implementations must preserve their internal visibility boundary.
func TestProviderAPIsHideProtocolDetails(t *testing.T) {
	t.Parallel()

	walkProductionGoFiles(t, modelsRoot(t), func(path string, fset *token.FileSet, file *ast.File) {
		// Only immediate provider packages: google/vertexai still defines public
		// types over its internal protocol.
		if strings.Count(path, "/") != 1 || containsPathSegment(path, "internal") || containsPathSegment(path, "protocol") {
			return
		}
		protocolAliases := importNamesWhere(t, path, file, func(importPath string) bool {
			return isSharedProtocolImport(importPath) ||
				strings.HasPrefix(importPath, modelsImportPrefix) && strings.Contains(importPath+"/", "/internal/protocol/")
		})
		if len(protocolAliases) == 0 {
			return
		}
		sharedProtocolAliases := importNamesWhere(t, path, file, isSharedProtocolImport)
		for _, declaration := range file.Decls {
			checkProtocolDeclaration(t, fset, path, declaration, protocolAliases, sharedProtocolAliases)
		}
	})
}

func checkProtocolDeclaration(
	t *testing.T,
	fset *token.FileSet,
	filename string,
	declaration ast.Decl,
	protocolAliases map[string]struct{},
	sharedProtocolAliases map[string]struct{},
) {
	t.Helper()
	switch value := declaration.(type) {
	case *ast.FuncDecl:
		if value.Name.IsExported() {
			rejectProtocolSelectors(t, fset, filename, value.Type, protocolAliases)
		}
	case *ast.GenDecl:
		for _, specification := range value.Specs {
			typeSpec, ok := specification.(*ast.TypeSpec)
			if ok {
				checkProtocolType(t, fset, filename, typeSpec, protocolAliases, sharedProtocolAliases)
			}
		}
	}
}

func checkProtocolType(
	t *testing.T,
	fset *token.FileSet,
	filename string,
	typeSpec *ast.TypeSpec,
	protocolAliases map[string]struct{},
	sharedProtocolAliases map[string]struct{},
) {
	t.Helper()
	if !typeSpec.Name.IsExported() ||
		(typeSpec.Assign.IsValid() && isImportedSelector(typeSpec.Type, sharedProtocolAliases)) {
		return
	}
	structure, ok := typeSpec.Type.(*ast.StructType)
	if !ok {
		rejectProtocolSelectors(t, fset, filename, typeSpec.Type, protocolAliases)
		return
	}
	for _, field := range structure.Fields.List {
		if len(field.Names) == 0 || field.Names[0].IsExported() {
			rejectProtocolSelectors(t, fset, filename, field.Type, protocolAliases)
		}
	}
}

func isImportedSelector(expression ast.Expr, aliases map[string]struct{}) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	qualifier, ok := selector.X.(*ast.Ident)
	if !ok {
		return false
	}
	_, imported := aliases[qualifier.Name]
	return imported
}

func rejectProtocolSelectors(t *testing.T, fset *token.FileSet, filename string, node ast.Node, aliases map[string]struct{}) {
	t.Helper()
	ast.Inspect(node, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		qualifier, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}
		if _, protocol := aliases[qualifier.Name]; protocol {
			t.Errorf("%s:%d exported API leaks protocol implementation type %s.%s", filename, fset.Position(selector.Pos()).Line, qualifier.Name, selector.Sel.Name)
		}
		return true
	})
}
