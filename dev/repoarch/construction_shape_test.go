package repoarch

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Constructors take context so each store can validate its backend before use.
// The config-free history/inmemory store needs neither an empty config nor a
// fallible construction contract.
func TestConfigurableStoresShareOneConstructionShape(t *testing.T) {
	t.Parallel()

	for _, family := range []string{
		"vectorstores", "historystores", "core/vectorstore/inmemory",
	} {
		t.Run(family, func(t *testing.T) {
			t.Parallel()
			assertStoreConstructionShape(t, family)
		})
	}
}

func assertStoreConstructionShape(t *testing.T, family string) {
	t.Helper()

	found := 0
	walkProductionGoFiles(t, filepath.Join(repositoryRoot(t), family), func(path string, fset *token.FileSet, file *ast.File) {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv != nil || function.Name.Name != "NewStore" {
				continue
			}
			found++
			location := family + "/" + path + ":" + strconv.Itoa(fset.Position(function.Pos()).Line)
			if !firstParameterIsContext(function) {
				t.Errorf("%s: NewStore does not take a context first, so it cannot confirm the backend it is pointed at", location)
			}
			if !secondParameterIsStoreConfig(function) {
				t.Errorf("%s: NewStore does not take a StoreConfig second", location)
			}
		}
	})
	if found == 0 {
		t.Fatalf("found no NewStore declarations under %s", family)
	}
}

func firstParameterIsContext(function *ast.FuncDecl) bool {
	parameters := function.Type.Params
	if parameters == nil || len(parameters.List) == 0 {
		return false
	}
	selector, ok := parameters.List[0].Type.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	packageName, ok := selector.X.(*ast.Ident)
	return ok && packageName.Name == "context" && selector.Sel.Name == "Context"
}

func secondParameterIsStoreConfig(function *ast.FuncDecl) bool {
	parameters := function.Type.Params
	if parameters == nil || len(parameters.List) < 2 {
		return false
	}
	identifier, ok := parameters.List[1].Type.(*ast.Ident)
	return ok && identifier.Name == "StoreConfig"
}

// Context is uniform across adapter constructors. Value constructors perform
// no I/O and are excluded.
func TestModelConstructorsTakeAContext(t *testing.T) {
	t.Parallel()

	clients := map[string]struct{}{
		"NewChat": {}, "NewMessages": {}, "NewResponses": {},
		"NewChatCompletions": {}, "NewCompatibleChatCompletions": {},
		"NewCompatibleMessages": {}, "NewTextCounter": {},
	}

	found := 0
	walkProductionGoFiles(t, modelsRoot(t), func(path string, fset *token.FileSet, file *ast.File) {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv != nil {
				continue
			}
			name := function.Name.Name
			_, isClient := clients[name]
			isModel := strings.HasPrefix(name, "New") && strings.HasSuffix(name, "Model")
			if !isClient && !isModel {
				continue
			}
			found++
			if !firstParameterIsContext(function) {
				t.Errorf("models/%s:%d: %s does not take a context first", path, fset.Position(function.Pos()).Line, name)
			}
		}
	})
	if found == 0 {
		t.Fatal("found no model constructors under models")
	}
}
