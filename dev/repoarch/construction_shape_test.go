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

	root := filepath.Join(repositoryRoot(t), family)
	fileSet := token.NewFileSet()
	found := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fileSet, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv != nil || function.Name.Name != "NewStore" {
				continue
			}
			found++
			position := fileSet.Position(function.Pos())
			location := filepath.ToSlash(path) + ":" + strconv.Itoa(position.Line)
			if !firstParameterIsContext(function) {
				t.Errorf("%s: NewStore does not take a context first, so it cannot confirm the backend it is pointed at", location)
			}
			if !secondParameterIsStoreConfig(function) {
				t.Errorf("%s: NewStore does not take a StoreConfig second", location)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", family, err)
	}
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
	if parameters == nil {
		return false
	}

	if len(parameters.List) < 2 {
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

	root := filepath.Join(repositoryRoot(t), "models")
	fileSet := token.NewFileSet()
	found := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fileSet, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
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
				position := fileSet.Position(function.Pos())
				t.Errorf("%s:%d: %s does not take a context first",
					filepath.ToSlash(path), position.Line, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk models: %v", err)
	}
	if found == 0 {
		t.Fatal("found no model constructors under models")
	}
}
