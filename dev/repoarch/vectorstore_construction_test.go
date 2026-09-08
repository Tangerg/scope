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

// Every vector store is constructed the same way: NewStore(ctx, StoreConfig).
//
// The shape is not cosmetic. A store that took no context could not read the
// backend it was pointed at, and four of them used to declare the live
// index's distance metric as an unchecked obligation on the caller — a wrong
// value there returns scores that are wrong rather than absent, which is the
// one failure nothing downstream can notice. Adding the check meant adding the
// context, so a store reintroducing the context-free form is also giving up
// the ability to confirm its own configuration.
//
// A caller should not have to remember which backend happens to be checkable
// either, so the parameter is required even where construction has nothing to
// read.
func TestVectorStoresShareOneConstructionShape(t *testing.T) {
	t.Parallel()

	root := filepath.Join(repositoryRoot(t), "vectorstores")
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
		t.Fatalf("walk vectorstores: %v", err)
	}
	if found == 0 {
		t.Fatal("found no NewStore declarations under vectorstores")
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
	// A signature may name both parameters in one field only if they share a
	// type, which context.Context and StoreConfig do not, so the second field
	// is the config.
	if len(parameters.List) < 2 {
		return false
	}
	identifier, ok := parameters.List[1].Type.(*ast.Ident)
	return ok && identifier.Name == "StoreConfig"
}
