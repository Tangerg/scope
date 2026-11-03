package repoarch

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var validatedModalityImports = map[string]struct{}{
	"github.com/Tangerg/scope/core/embedding":     {},
	"github.com/Tangerg/scope/core/image":         {},
	"github.com/Tangerg/scope/core/moderation":    {},
	"github.com/Tangerg/scope/core/rerank":        {},
	"github.com/Tangerg/scope/core/speech":        {},
	"github.com/Tangerg/scope/core/transcription": {},
}

func TestValidatedNonChatModalityImportsCoverEveryCoreModelSPI(t *testing.T) {
	t.Parallel()

	coreRoot := filepath.Join(repositoryRoot(t), "core")
	entries, err := os.ReadDir(coreRoot)
	if err != nil {
		t.Fatal(err)
	}
	want := make([]string, 0, len(validatedModalityImports))
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "internal" || entry.Name() == "chat" {
			continue
		}
		for _, file := range parseImmediateProductionFiles(t, filepath.Join(coreRoot, entry.Name())) {
			if publishesModelInterface(file) {
				want = append(want, "github.com/Tangerg/scope/core/"+entry.Name())
				break
			}
		}
	}
	slices.Sort(want)
	got := slices.Sorted(maps.Keys(validatedModalityImports))
	if !slices.Equal(got, want) {
		t.Fatalf("validated modality imports = %v, Core Model SPI imports = %v", got, want)
	}
}

func publishesModelInterface(file *ast.File) bool {
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.TYPE {
			continue
		}
		for _, specification := range general.Specs {
			typeSpec := specification.(*ast.TypeSpec)
			if typeSpec.Name.Name == "Model" {
				_, isInterface := typeSpec.Type.(*ast.InterfaceType)
				return isInterface
			}
		}
	}
	return false
}

// Delegation is allowed because the canonical method is checked separately.
func TestModalityModelBoundariesValidateRequests(t *testing.T) {
	t.Parallel()

	walkProductionGoFiles(t, modelsRoot(t), func(path string, fset *token.FileSet, file *ast.File) {
		aliases := modalityImportAliases(t, path, file)
		if len(aliases) == 0 {
			return
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv == nil || function.Body == nil || (function.Name.Name != "Call" && function.Name.Name != "Stream") {
				continue
			}
			requestName, ok := coreRequestParameter(function, aliases)
			if !ok {
				continue
			}
			if !validatesOrDelegates(function.Body, requestName) {
				t.Errorf("%s:%d %s must validate %s before crossing the provider boundary", path, fset.Position(function.Pos()).Line, function.Name.Name, requestName)
			}
		}
	})
}

func TestProviderExtensionKeysAreSemanticAndNamespaced(t *testing.T) {
	t.Parallel()

	constants := extensionKeyConstants{}
	walkProductionGoFiles(t, modelsRoot(t), func(path string, _ *token.FileSet, file *ast.File) {
		constants.collect(path, file)
	})
	walkProductionGoFiles(t, modelsRoot(t), func(path string, fset *token.FileSet, file *ast.File) {
		prefix := extensionProvider(path) + "/"
		for _, declaration := range file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.CONST {
				continue
			}
			for _, specification := range general.Specs {
				checkExtensionValueSpec(t, fset, path, prefix, specification.(*ast.ValueSpec), constants)
			}
		}
	})
}

func checkExtensionValueSpec(
	t *testing.T,
	fset *token.FileSet,
	relative string,
	prefix string,
	values *ast.ValueSpec,
	constants extensionKeyConstants,
) {
	t.Helper()
	for index, name := range values.Names {
		if name.Name == "OptionsKey" {
			t.Errorf("%s:%d use a modality-specific RequestExtensionKey name instead of OptionsKey", relative, fset.Position(name.Pos()).Line)
			continue
		}
		if !strings.HasSuffix(name.Name, "ExtensionKey") || index >= len(values.Values) {
			continue
		}
		resolved, err := constants.resolve(filepath.Dir(relative), name.Name, make(map[string]bool))
		if err != nil || resolved.Kind() != constant.String {
			t.Errorf("%s:%d %s must resolve to a string constant: %v", relative, fset.Position(name.Pos()).Line, name.Name, err)
			continue
		}
		value := constant.StringVal(resolved)
		if !strings.HasPrefix(value, prefix) {
			t.Errorf("%s:%d %s = %q, want prefix %q", relative, fset.Position(name.Pos()).Line, name.Name, value, prefix)
		}
		if strings.HasSuffix(value, "/options") {
			t.Errorf("%s:%d %s = %q is ambiguous; name the request modality", relative, fset.Position(name.Pos()).Line, name.Name, value)
		}
	}
}

type extensionKeyConstant struct {
	expression ast.Expr
	imports    map[string]string
}

// A derived key must satisfy the same invariant as a literal. Resolution follows
// its declared owner, including protocol constants imported by provider leaves.
type extensionKeyConstants map[string]map[string]extensionKeyConstant

func (e extensionKeyConstants) collect(path string, file *ast.File) {
	directory := filepath.Dir(path)
	if e[directory] == nil {
		e[directory] = make(map[string]extensionKeyConstant)
	}
	imports := make(map[string]string)
	for _, imported := range file.Imports {
		path, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			continue
		}
		alias := filepath.Base(path)
		if imported.Name != nil {
			alias = imported.Name.Name
		}
		imports[alias] = path
	}
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.CONST {
			continue
		}
		for _, specification := range general.Specs {
			values := specification.(*ast.ValueSpec)
			for index, name := range values.Names {
				if index < len(values.Values) {
					e[directory][name.Name] = extensionKeyConstant{expression: values.Values[index], imports: imports}
				}
			}
		}
	}
}

func (e extensionKeyConstants) resolve(directory, name string, active map[string]bool) (constant.Value, error) {
	identity := directory + "/" + name
	definition, found := e[directory][name]
	if !found || active[identity] {
		return constant.MakeUnknown(), fmt.Errorf("unresolved or cyclic constant %s", identity)
	}
	active[identity] = true
	defer delete(active, identity)
	return e.evaluate(directory, definition.imports, definition.expression, active)
}

func (e extensionKeyConstants) evaluate(directory string, imports map[string]string, expression ast.Expr, active map[string]bool) (constant.Value, error) {
	switch value := expression.(type) {
	case *ast.BasicLit:
		return constant.MakeFromLiteral(value.Value, value.Kind, 0), nil
	case *ast.ParenExpr:
		return e.evaluate(directory, imports, value.X, active)
	case *ast.Ident:
		return e.resolve(directory, value.Name, active)
	case *ast.SelectorExpr:
		qualifier, ok := value.X.(*ast.Ident)
		if ok {
			const modelsImportPrefix = "github.com/Tangerg/scope/models/"
			if path, found := strings.CutPrefix(imports[qualifier.Name], modelsImportPrefix); found {
				return e.resolve(filepath.FromSlash(path), value.Sel.Name, active)
			}
		}
	case *ast.BinaryExpr:
		left, err := e.evaluate(directory, imports, value.X, active)
		if err != nil {
			return constant.MakeUnknown(), err
		}
		right, err := e.evaluate(directory, imports, value.Y, active)
		if err != nil {
			return constant.MakeUnknown(), err
		}
		if value.Op == token.ADD && left.Kind() == constant.String && right.Kind() == constant.String {
			return constant.BinaryOp(left, value.Op, right), nil
		}
	}
	return constant.MakeUnknown(), fmt.Errorf("unsupported extension-key expression %T", expression)
}

func extensionProvider(relative string) string {
	parts := strings.Split(relative, "/")
	if len(parts) >= 3 && parts[0] == "protocol" {
		return parts[1]
	}
	if len(parts) >= 3 && parts[1] != "internal" {
		return parts[1]
	}
	return parts[0]
}

func TestModalityOptionsUseValueSemantics(t *testing.T) {
	t.Parallel()

	walkProductionGoFiles(t, modelsRoot(t), func(path string, fset *token.FileSet, file *ast.File) {
		aliases := modalityImportAliases(t, path, file)
		if len(aliases) == 0 {
			return
		}
		ast.Inspect(file, func(node ast.Node) bool {
			pointer, ok := node.(*ast.StarExpr)
			if !ok {
				return true
			}
			selector, ok := pointer.X.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "Options" {
				return true
			}
			qualifier, ok := selector.X.(*ast.Ident)
			if !ok {
				return true
			}
			if _, target := aliases[qualifier.Name]; target {
				t.Errorf("%s:%d modality Options must use value semantics", path, fset.Position(pointer.Pos()).Line)
			}
			return true
		})
	})
}

func TestModelsCloneOwnedDefaultOptions(t *testing.T) {
	t.Parallel()

	walkProductionGoFiles(t, modelsRoot(t), func(path string, fset *token.FileSet, file *ast.File) {
		if len(modalityImportAliases(t, path, file)) == 0 {
			return
		}
		ast.Inspect(file, func(node ast.Node) bool {
			field, ok := node.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			name, ok := field.Key.(*ast.Ident)
			if !ok || name.Name != "defaultOptions" {
				return true
			}
			call, ok := field.Value.(*ast.CallExpr)
			if !ok {
				t.Errorf("%s:%d owned defaultOptions must be cloned", path, fset.Position(field.Value.Pos()).Line)
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "Clone" || len(call.Args) != 0 {
				t.Errorf("%s:%d owned defaultOptions must be assigned from Clone()", path, fset.Position(field.Value.Pos()).Line)
			}
			return true
		})
	})
}

func modelsRoot(t *testing.T) string {
	t.Helper()
	return filepath.Join(repositoryRoot(t), "models")
}

func modalityImportAliases(t *testing.T, path string, file *ast.File) map[string]struct{} {
	t.Helper()
	return importNamesWhere(t, path, file, func(importPath string) bool {
		_, validated := validatedModalityImports[importPath]
		return validated
	})
}

func coreRequestParameter(function *ast.FuncDecl, aliases map[string]struct{}) (string, bool) {
	for _, field := range function.Type.Params.List {
		pointer, ok := field.Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		selector, ok := pointer.X.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Request" {
			continue
		}
		qualifier, ok := selector.X.(*ast.Ident)
		if !ok {
			continue
		}
		if _, target := aliases[qualifier.Name]; !target || len(field.Names) != 1 {
			continue
		}
		return field.Names[0].Name, true
	}
	return "", false
}

func validatesOrDelegates(body *ast.BlockStmt, requestName string) bool {
	found := false
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return !found
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return !found
		}
		if receiver, ok := selector.X.(*ast.Ident); ok && receiver.Name == requestName && selector.Sel.Name == "Validate" {
			found = true
			return false
		}
		if selector.Sel.Name != "Call" && selector.Sel.Name != "Stream" {
			return !found
		}
		for _, argument := range call.Args {
			if identifier, ok := argument.(*ast.Ident); ok && identifier.Name == requestName {
				found = true
				return false
			}
		}
		return !found
	})
	return found
}
