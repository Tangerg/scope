package repoarch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var retiredProviderSymbols = map[string]struct{}{
	"API":                 {},
	"APIConfig":           {},
	"AnthropicChat":       {},
	"AnthropicChatConfig": {},
	"FetchNative":         {},
	"NewAPI":              {},
	"NewAnthropicChat":    {},
	"NewOpenAIChat":       {},
	"NewResponsesChat":    {},
	"OpenAIChat":          {},
	"OpenAIChatConfig":    {},
	"Request":             {},
	"Response":            {},
	"ResponsesChat":       {},
	"SearchNative":        {},
}

var retiredProtocolChatSymbols = map[string]struct{}{
	"Chat":              {},
	"ChatConfig":        {},
	"NewChat":           {},
	"NewCompatibleChat": {},
}

var coreOwnedChatOptionSymbols = map[string]struct{}{
	"ToolChoice":      {},
	"ToolChoiceMode":  {},
	"ToolParallelism": {},
}

// An obsolete Core name must not remain forbidden for provider-owned fields.
func TestCoreOwnedChatOptionSymbolsAreStillCoreOwned(t *testing.T) {
	t.Parallel()

	owned := make(map[string]struct{})
	for _, file := range parseImmediateProductionFiles(t, filepath.Join(repositoryRoot(t), "core", "chat")) {
		for _, declaration := range file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.TYPE {
				continue
			}
			for _, specification := range general.Specs {
				typeSpec := specification.(*ast.TypeSpec)
				if !typeSpec.Name.IsExported() {
					continue
				}
				owned[typeSpec.Name.Name] = struct{}{}
				structure, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					continue
				}
				for _, field := range structure.Fields.List {
					for _, name := range field.Names {
						if name.IsExported() {
							owned[name.Name] = struct{}{}
						}
					}
				}
			}
		}
	}

	for symbol := range coreOwnedChatOptionSymbols {
		if _, found := owned[symbol]; !found {
			t.Errorf("coreOwnedChatOptionSymbols lists %s, which core/chat no longer exports", symbol)
		}
	}
}

// Exact shared wire implementations need no forwarding wrapper. Private
// protocol wrappers remain necessary for Go internal visibility.
func TestSharedProtocolsArePromotedWithoutDelegatingWrappers(t *testing.T) {
	t.Parallel()

	walkProductionGoFiles(t, modelsRoot(t), func(path string, fset *token.FileSet, file *ast.File) {
		if containsPathSegment(path, "internal") || containsPathSegment(path, "protocol") {
			return
		}
		protocols := importNamesWhere(t, path, file, isSharedProtocolImport)
		if len(protocols) == 0 {
			return
		}
		for _, declaration := range file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, specification := range general.Specs {
				checkSharedProtocolType(t, fset, path, protocols, specification)
			}
		}
	})
}

func checkSharedProtocolType(
	t *testing.T,
	fset *token.FileSet,
	path string,
	protocols map[string]struct{},
	specification ast.Spec,
) {
	t.Helper()
	typeSpec, ok := specification.(*ast.TypeSpec)
	if !ok {
		return
	}
	structure, ok := typeSpec.Type.(*ast.StructType)
	if !ok || len(structure.Fields.List) != 1 {
		return
	}
	field := structure.Fields.List[0]
	if len(field.Names) != 1 || field.Names[0].Name != "protocol" ||
		!pointsToImportedSelector(field.Type, protocols) {
		return
	}
	t.Errorf("%s:%d %s is a behaviorless shared-protocol wrapper; promote the protocol model with a type alias", filepath.ToSlash(path), fset.Position(typeSpec.Pos()).Line, typeSpec.Name.Name)
}

func TestModelProvidersOwnTheirPublicSurface(t *testing.T) {
	t.Parallel()

	for _, dir := range modelProviderDirectories(t) {
		assertOwnedPublicSurface(t, dir, retiredProviderSymbols)
	}

	root := modelsRoot(t)
	// Reusable wire protocols are public infrastructure rather than provider
	// facades, but their semantic APIs must still hide SDK-owned types.
	assertOwnedPublicSurface(t, filepath.Join(root, "protocol", "anthropic"), retiredProtocolChatSymbols)
	assertOwnedPublicSurface(t, filepath.Join(root, "protocol", "openai"), retiredProtocolChatSymbols)
}

func TestModelProvidersDoNotDuplicateCoreChatOptions(t *testing.T) {
	t.Parallel()

	root := modelsRoot(t)
	dirs := append(modelProviderDirectories(t),
		filepath.Join(root, "protocol", "anthropic"),
		filepath.Join(root, "protocol", "openai"),
	)
	for _, dir := range dirs {
		assertNoCoreOwnedChatOptions(t, dir)
	}
}

func modelProviderDirectories(t *testing.T) []string {
	t.Helper()
	root := modelsRoot(t)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "catalog" || entry.Name() == "internal" || entry.Name() == "protocol" {
			continue
		}
		if dir := filepath.Join(root, entry.Name()); hasProductionGoFiles(t, dir) {
			dirs = append(dirs, dir)
		}
	}
	if len(dirs) == 0 {
		t.Fatal("discovered no model provider directories")
	}
	return dirs
}

func assertNoCoreOwnedChatOptions(t *testing.T, dir string) {
	t.Helper()
	for _, file := range parseImmediateProductionFiles(t, dir) {
		for _, declaration := range file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.TYPE {
				continue
			}
			for _, specification := range general.Specs {
				typeSpec := specification.(*ast.TypeSpec)
				if !typeSpec.Name.IsExported() {
					continue
				}
				if _, duplicate := coreOwnedChatOptionSymbols[typeSpec.Name.Name]; duplicate {
					t.Errorf("%s exports Core-owned chat option %s", filepath.ToSlash(dir), typeSpec.Name.Name)
				}
				structure, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					continue
				}
				for _, field := range structure.Fields.List {
					for _, name := range field.Names {
						if _, duplicate := coreOwnedChatOptionSymbols[name.Name]; duplicate {
							t.Errorf("%s exported %s duplicates a Core-owned chat option", filepath.ToSlash(dir), name.Name)
						}
					}
				}
			}
		}
	}
}

func TestWebProvidersExposeOnlyNormalizedTransport(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	family := "tools/web"
	familyDir := filepath.Join(root, filepath.FromSlash(family))
	entries, err := os.ReadDir(familyDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "internal" {
			continue
		}
		dir := filepath.Join(familyDir, entry.Name())
		if !hasProductionGoFiles(t, dir) {
			continue
		}
		t.Run(strings.ReplaceAll(family+"/"+entry.Name(), "/", "_"), func(t *testing.T) {
			assertOwnedPublicSurface(t, dir, retiredProviderSymbols)
			assertProviderTestsAreOffline(t, dir)
		})
	}
}

func TestFixedConstructionStateUsesConfig(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	for _, relative := range []string{
		"core/chatclient",
		"etl/html",
		"etl/markdown",
		"etl/pdf",
	} {
		dir := filepath.Join(root, filepath.FromSlash(relative))
		for _, file := range parseImmediateProductionFiles(t, dir) {
			for _, declaration := range file.Decls {
				switch value := declaration.(type) {
				case *ast.FuncDecl:
					if value.Name.IsExported() && strings.HasPrefix(value.Name.Name, "With") {
						t.Errorf("%s exports %s; fixed construction state belongs in Config", relative, value.Name.Name)
					}
				case *ast.GenDecl:
					for _, specification := range value.Specs {
						typeSpec, ok := specification.(*ast.TypeSpec)
						if ok && typeSpec.Name.Name == "Option" {
							t.Errorf("%s exports Option; fixed construction state belongs in Config", relative)
						}
					}
				}
			}
		}
	}
}

func assertOwnedPublicSurface(t *testing.T, dir string, retired map[string]struct{}) {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		thirdParty := importNamesWhere(t, path, file, isExternalImport)
		for _, declaration := range file.Decls {
			checkOwnedPublicDeclaration(t, fset, path, declaration, retired, thirdParty)
		}
	}
}

func checkOwnedPublicDeclaration(
	t *testing.T,
	fset *token.FileSet,
	path string,
	declaration ast.Decl,
	retired map[string]struct{},
	thirdParty map[string]struct{},
) {
	t.Helper()
	switch value := declaration.(type) {
	case *ast.FuncDecl:
		if !value.Name.IsExported() || !receiverIsPublic(value.Recv) {
			return
		}
		rejectRetiredProviderSymbol(t, fset, path, value.Name, retired)
		rejectThirdPartySelectors(t, fset, path, value.Type, thirdParty)
	case *ast.GenDecl:
		for _, specification := range value.Specs {
			checkOwnedPublicSpec(t, fset, path, specification, retired, thirdParty)
		}
	}
}

func checkOwnedPublicSpec(
	t *testing.T,
	fset *token.FileSet,
	path string,
	specification ast.Spec,
	retired map[string]struct{},
	thirdParty map[string]struct{},
) {
	t.Helper()
	switch spec := specification.(type) {
	case *ast.TypeSpec:
		if !spec.Name.IsExported() {
			return
		}
		rejectRetiredProviderSymbol(t, fset, path, spec.Name, retired)
		structure, ok := spec.Type.(*ast.StructType)
		if !ok {
			rejectThirdPartySelectors(t, fset, path, spec.Type, thirdParty)
			return
		}
		for _, field := range structure.Fields.List {
			if len(field.Names) == 0 || hasExportedName(field.Names) {
				rejectThirdPartySelectors(t, fset, path, field.Type, thirdParty)
			}
		}
	case *ast.ValueSpec:
		if spec.Type != nil && hasExportedName(spec.Names) {
			rejectThirdPartySelectors(t, fset, path, spec.Type, thirdParty)
		}
	}
}

func receiverIsPublic(receiver *ast.FieldList) bool {
	if receiver == nil {
		return true
	}
	if len(receiver.List) != 1 {
		return false
	}
	typeExpression := receiver.List[0].Type
	if pointer, ok := typeExpression.(*ast.StarExpr); ok {
		typeExpression = pointer.X
	}
	identifier, ok := typeExpression.(*ast.Ident)
	return ok && identifier.IsExported()
}

func assertProviderTestsAreOffline(t *testing.T, dir string) {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		found = true
		path := filepath.Join(dir, entry.Name())
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		osAliases := importNamesWhere(t, path, file, func(importPath string) bool { return importPath == "os" })
		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			qualifier, ok := selector.X.(*ast.Ident)
			if !ok {
				return true
			}
			if _, imported := osAliases[qualifier.Name]; imported && (selector.Sel.Name == "Getenv" || selector.Sel.Name == "LookupEnv" || selector.Sel.Name == "Environ") {
				t.Errorf("%s:%d provider tests must be offline; found os.%s", filepath.ToSlash(path), fset.Position(selector.Pos()).Line, selector.Sel.Name)
			}
			return true
		})
	}
	if !found {
		t.Errorf("%s has no provider-owned tests", filepath.ToSlash(dir))
	}
}

func parseImmediateProductionFiles(t *testing.T, dir string) []*ast.File {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}
	return files
}

func isSharedProtocolImport(importPath string) bool {
	return strings.HasPrefix(importPath, modelsImportPrefix+"protocol/")
}

func isExternalImport(importPath string) bool {
	return !isRepositoryImport(importPath) && isThirdPartyImport(importPath)
}

func pointsToImportedSelector(expression ast.Expr, aliases map[string]struct{}) bool {
	pointer, ok := expression.(*ast.StarExpr)
	if !ok {
		return false
	}
	selector, ok := pointer.X.(*ast.SelectorExpr)
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

func rejectRetiredProviderSymbol(t *testing.T, fset *token.FileSet, path string, name *ast.Ident, retired map[string]struct{}) {
	t.Helper()
	if _, forbidden := retired[name.Name]; forbidden {
		t.Errorf("%s:%d exports retired transport symbol %s", filepath.ToSlash(path), fset.Position(name.Pos()).Line, name.Name)
	}
}

func rejectThirdPartySelectors(t *testing.T, fset *token.FileSet, path string, node ast.Node, aliases map[string]struct{}) {
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
		if _, thirdParty := aliases[qualifier.Name]; thirdParty {
			t.Errorf("%s:%d public API leaks third-party type %s.%s", filepath.ToSlash(path), fset.Position(selector.Pos()).Line, qualifier.Name, selector.Sel.Name)
		}
		return true
	})
}

func hasExportedName(names []*ast.Ident) bool {
	for _, name := range names {
		if name.IsExported() {
			return true
		}
	}
	return false
}
