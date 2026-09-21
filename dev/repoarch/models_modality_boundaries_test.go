package repoarch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
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

// TestModalityModelBoundariesValidateRequests prevents adapters from
// dereferencing or translating a Core request before its complete protocol
// value has been checked. A convenience method may delegate to the validated
// canonical method; the delegated method is checked independently.
func TestModalityModelBoundariesValidateRequests(t *testing.T) {
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

		file, err := parser.ParseFile(fset, filename, nil, 0)
		if err != nil {
			return err
		}
		aliases := modalityImportAliases(file)
		if len(aliases) == 0 {
			return nil
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
			if validatesOrDelegates(function.Body, requestName) {
				continue
			}
			relative, err := filepath.Rel(root, filename)
			if err != nil {
				return err
			}
			t.Errorf("%s:%d %s must validate %s before crossing the provider boundary", filepath.ToSlash(relative), fset.Position(function.Pos()).Line, function.Name.Name, requestName)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestProviderExtensionKeysAreSemanticAndNamespaced(t *testing.T) {
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
		return checkProviderExtensionKeys(t, root, fset, filename)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func checkProviderExtensionKeys(t *testing.T, root string, fset *token.FileSet, filename string) error {
	t.Helper()
	file, err := parser.ParseFile(fset, filename, nil, 0)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, filename)
	if err != nil {
		return err
	}
	prefix := extensionProvider(relative) + "/"
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.CONST {
			continue
		}
		for _, specification := range general.Specs {
			checkExtensionValueSpec(t, fset, relative, prefix, specification.(*ast.ValueSpec))
		}
	}
	return nil
}

func checkExtensionValueSpec(
	t *testing.T,
	fset *token.FileSet,
	relative string,
	prefix string,
	values *ast.ValueSpec,
) {
	t.Helper()
	for index, name := range values.Names {
		if name.Name == "OptionsKey" {
			t.Errorf("%s:%d use a modality-specific RequestExtensionKey name instead of OptionsKey", filepath.ToSlash(relative), fset.Position(name.Pos()).Line)
			continue
		}
		if !strings.HasSuffix(name.Name, "ExtensionKey") || index >= len(values.Values) {
			continue
		}
		literal, ok := values.Values[index].(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			t.Errorf("%s:%d %s must be a string literal", filepath.ToSlash(relative), fset.Position(name.Pos()).Line, name.Name)
			continue
		}
		value, err := strconv.Unquote(literal.Value)
		if err != nil || !strings.HasPrefix(value, prefix) {
			t.Errorf("%s:%d %s = %q, want prefix %q", filepath.ToSlash(relative), fset.Position(name.Pos()).Line, name.Name, value, prefix)
		}
		if strings.HasSuffix(value, "/options") {
			t.Errorf("%s:%d %s = %q is ambiguous; name the request modality", filepath.ToSlash(relative), fset.Position(name.Pos()).Line, name.Name, value)
		}
	}
}

func extensionProvider(relative string) string {
	parts := strings.Split(filepath.ToSlash(relative), "/")
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

	root := modelsRoot(t)
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(filename, ".go") || strings.HasSuffix(filename, "_test.go") {
			return nil
		}

		file, err := parser.ParseFile(fset, filename, nil, 0)
		if err != nil {
			return err
		}
		aliases := modalityImportAliases(file)
		if len(aliases) == 0 {
			return nil
		}
		relative, err := filepath.Rel(root, filename)
		if err != nil {
			return err
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
				t.Errorf("%s:%d modality Options must use value semantics", filepath.ToSlash(relative), fset.Position(pointer.Pos()).Line)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestModelsCloneOwnedDefaultOptions(t *testing.T) {
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

		file, err := parser.ParseFile(fset, filename, nil, 0)
		if err != nil {
			return err
		}
		if len(modalityImportAliases(file)) == 0 {
			return nil
		}
		relative, err := filepath.Rel(root, filename)
		if err != nil {
			return err
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
				t.Errorf("%s:%d owned defaultOptions must be cloned", filepath.ToSlash(relative), fset.Position(field.Value.Pos()).Line)
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "Clone" || len(call.Args) != 0 {
				t.Errorf("%s:%d owned defaultOptions must be assigned from Clone()", filepath.ToSlash(relative), fset.Position(field.Value.Pos()).Line)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func modelsRoot(t *testing.T) string {
	t.Helper()
	return filepath.Join(repositoryRoot(t), "models")
}

func modalityImportAliases(file *ast.File) map[string]struct{} {
	aliases := make(map[string]struct{})
	for _, imported := range file.Imports {
		pathValue, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			continue
		}
		if _, target := validatedModalityImports[pathValue]; !target {
			continue
		}
		name := filepath.Base(pathValue)
		if imported.Name != nil {
			name = imported.Name.Name
		}
		aliases[name] = struct{}{}
	}
	return aliases
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
