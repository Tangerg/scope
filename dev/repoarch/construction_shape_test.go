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

// Every store is constructed the same way: NewStore(ctx, StoreConfig). That
// includes Core's in-memory reference implementation, so it is a drop-in for a
// backend one rather than a store whose call site has to be rewritten when a
// caller moves off it.
//
// The shape is not cosmetic. A store that took no context could not read the
// backend it was pointed at, and several of them used to declare a fact about
// the live backend — a vector index's distance metric, a Cosmos container's
// partition-key path — as an unchecked obligation on the caller. A wrong
// metric returns scores that are wrong rather than absent, which is the one
// failure nothing downstream can notice. Adding the check meant adding the
// context, so a store reintroducing the context-free form is also giving up
// the ability to confirm its own configuration.
//
// A caller should not have to remember which backend happens to be checkable
// either, so the parameter is required even where construction has nothing to
// read.
func TestStoresShareOneConstructionShape(t *testing.T) {
	t.Parallel()

	for _, family := range []string{"vectorstores", "historystores", "core/vectorstore/inmemory"} {
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
	// A signature may name both parameters in one field only if they share a
	// type, which context.Context and StoreConfig do not, so the second field
	// is the config.
	if len(parameters.List) < 2 {
		return false
	}
	identifier, ok := parameters.List[1].Type.(*ast.Ident)
	return ok && identifier.Name == "StoreConfig"
}

// Every model adapter constructor takes a context first, for the same reason
// the stores do: two of them already had to (google and bedrock, because their
// SDKs build a client from one), and a caller should not have to remember which
// provider happens to need it. The parameter is unused in most of them today
// and named _ there, which says so; what it buys is that an adapter that later
// needs to reach the provider at construction can do it without changing its
// signature.
//
// Value constructors are excluded. NewTextPart and NewThinkingPart build a
// datum out of arguments and reach nothing, so a context there would be noise.
func TestModelConstructorsTakeAContext(t *testing.T) {
	t.Parallel()

	clients := map[string]struct{}{
		"NewChat": {}, "NewMessages": {}, "NewResponses": {},
		"NewChatCompletions": {}, "NewCompatibleChatCompletions": {},
		"NewCompatibleMessages": {}, "NewTextEstimator": {},
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
