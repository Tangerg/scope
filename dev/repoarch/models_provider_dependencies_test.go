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

// A shared protocol model has one public name, in its protocol package:
// providers return it directly and never re-declare it under an alias.
// Provider-private protocol implementations stay behind the internal
// visibility boundary.
func TestProviderAPIsHideProtocolDetails(t *testing.T) {
	t.Parallel()

	walkProductionGoFiles(t, modelsRoot(t), func(path string, fset *token.FileSet, file *ast.File) {
		// Only immediate provider packages: google/vertexai still defines public
		// types over its internal protocol.
		if strings.Count(path, "/") != 1 || containsPathSegment(path, "internal") || containsPathSegment(path, "protocol") {
			return
		}
		sharedProtocols := importNamesWhere(t, path, file, isSharedProtocolImport)
		privateProtocols := importNamesWhere(t, path, file, func(importPath string) bool {
			return strings.HasPrefix(importPath, modelsImportPrefix) && strings.Contains(importPath+"/", "/internal/protocol/")
		})
		for _, declaration := range file.Decls {
			checkProtocolDeclaration(t, fset, path, declaration, sharedProtocols, privateProtocols)
		}
	})
}

func checkProtocolDeclaration(
	t *testing.T,
	fset *token.FileSet,
	filename string,
	declaration ast.Decl,
	sharedProtocols map[string]struct{},
	privateProtocols map[string]struct{},
) {
	t.Helper()
	switch value := declaration.(type) {
	case *ast.FuncDecl:
		if value.Name.IsExported() {
			rejectProtocolSelectors(t, fset, filename, value.Type, privateProtocols)
		}
	case *ast.GenDecl:
		for _, specification := range value.Specs {
			typeSpec, ok := specification.(*ast.TypeSpec)
			if ok {
				checkProtocolType(t, fset, filename, typeSpec, sharedProtocols, privateProtocols)
			}
		}
	}
}

func checkProtocolType(
	t *testing.T,
	fset *token.FileSet,
	filename string,
	typeSpec *ast.TypeSpec,
	sharedProtocols map[string]struct{},
	privateProtocols map[string]struct{},
) {
	t.Helper()
	if !typeSpec.Name.IsExported() {
		return
	}
	if typeSpec.Assign.IsValid() && isImportedSelector(typeSpec.Type, sharedProtocols) {
		t.Errorf("%s:%d %s aliases a shared protocol model; return the protocol type under its own name", filename, fset.Position(typeSpec.Pos()).Line, typeSpec.Name.Name)
		return
	}
	structure, ok := typeSpec.Type.(*ast.StructType)
	if !ok {
		rejectProtocolSelectors(t, fset, filename, typeSpec.Type, privateProtocols)
		return
	}
	for _, field := range structure.Fields.List {
		if len(field.Names) == 0 || field.Names[0].IsExported() {
			rejectProtocolSelectors(t, fset, filename, field.Type, privateProtocols)
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
