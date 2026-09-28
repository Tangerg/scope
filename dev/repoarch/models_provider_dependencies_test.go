package repoarch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const modelsImportPrefix = "github.com/Tangerg/scope/models/"

func TestProviderDependenciesAreOneWay(t *testing.T) {
	t.Parallel()

	root := modelsRoot(t)
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(filename, ".go") || strings.HasSuffix(filename, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, filename)
		if err != nil {
			return err
		}
		source, _, _ := strings.Cut(filepath.ToSlash(relative), "/")
		file, err := parser.ParseFile(fset, filename, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imported := range file.Imports {
			pathValue, err := strconv.Unquote(imported.Path.Value)
			if err != nil || !strings.HasPrefix(pathValue, modelsImportPrefix) {
				continue
			}
			target := strings.TrimPrefix(pathValue, modelsImportPrefix)
			targetRoot, _, _ := strings.Cut(target, "/")
			switch {
			case source == "internal" && targetRoot != "internal" && targetRoot != "protocol":
				t.Errorf("%s:%d internal implementation must not depend on public provider %q", filepath.ToSlash(relative), fset.Position(imported.Pos()).Line, targetRoot)
			case source != "internal" && targetRoot != "internal" && targetRoot != "protocol" && targetRoot != source:
				t.Errorf("%s:%d provider %q must not depend on peer provider %q", filepath.ToSlash(relative), fset.Position(imported.Pos()).Line, source, targetRoot)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Exact shared protocols may be promoted by alias; provider-private
// implementations must preserve their internal visibility boundary.
func TestProviderAPIsHideProtocolDetails(t *testing.T) {
	t.Parallel()

	root := modelsRoot(t)
	entries, err := filepath.Glob(filepath.Join(root, "*", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, filename := range entries {
		if strings.HasSuffix(filename, "_test.go") {
			continue
		}
		checkProviderAPIFile(t, fset, filename)
	}
}

func checkProviderAPIFile(t *testing.T, fset *token.FileSet, filename string) {
	t.Helper()
	file, err := parser.ParseFile(fset, filename, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	protocolAliases, sharedProtocolAliases := protocolImportAliases(file)
	if len(protocolAliases) == 0 {
		return
	}
	for _, declaration := range file.Decls {
		checkProtocolDeclaration(
			t, fset, filename, declaration, protocolAliases, sharedProtocolAliases,
		)
	}
}

func protocolImportAliases(file *ast.File) (map[string]struct{}, map[string]struct{}) {
	protocolAliases := make(map[string]struct{})
	sharedProtocolAliases := make(map[string]struct{})
	for _, imported := range file.Imports {
		pathValue, err := strconv.Unquote(imported.Path.Value)
		if err != nil || !strings.HasPrefix(pathValue, modelsImportPrefix) {
			continue
		}
		relativeImport := strings.TrimPrefix(pathValue, modelsImportPrefix)
		shared := strings.HasPrefix(relativeImport, "protocol/")
		private := strings.Contains("/"+relativeImport+"/", "/internal/protocol/")
		if !shared && !private {
			continue
		}
		name := filepath.Base(pathValue)
		if imported.Name != nil {
			name = imported.Name.Name
		}
		protocolAliases[name] = struct{}{}
		if shared {
			sharedProtocolAliases[name] = struct{}{}
		}
	}
	return protocolAliases, sharedProtocolAliases
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
			t.Errorf("%s:%d exported API leaks protocol implementation type %s.%s", filepath.Base(filename), fset.Position(selector.Pos()).Line, qualifier.Name, selector.Sel.Name)
		}
		return true
	})
}
