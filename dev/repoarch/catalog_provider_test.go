package repoarch

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The catalog states the rule its rows depend on: "provider equals the
// adapter's Provider constant, lowercased". Nothing enforced it. A key that
// drifts from its adapter's constant does not fail anywhere — Lookup simply
// answers not-found for every caller that passes the constant, and a cost
// attribution silently becomes zero.
//
// The check lives here rather than in the catalog because the catalog holds no
// provider module and must not start importing twenty of them to learn their
// names. Reading the constants is what this module already does.
func TestCatalogProviderKeysMatchTheirAdapterConstants(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	keys := catalogProviderKeys(t, root)
	constants := adapterProviderConstants(t, root)

	for key, file := range keys {
		if _, found := constants[key]; !found {
			t.Errorf("catalog %s keys provider %q, which no adapter declares as its Provider constant", file, key)
		}
	}
}

// catalogProviderKeys reads the provider key out of every embedded config,
// returning key -> file name.
func catalogProviderKeys(t *testing.T, root string) map[string]string {
	t.Helper()

	directory := filepath.Join(root, "models", "catalog", "configs")
	names, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read catalog configs: %v", err)
	}
	keys := make(map[string]string, len(names))
	for _, name := range names {
		if name.IsDir() || filepath.Ext(name.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(directory, name.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", name.Name(), err)
		}
		var entry struct {
			Provider string `json:"provider"`
		}
		if err := json.Unmarshal(raw, &entry); err != nil {
			t.Fatalf("decode %s: %v", name.Name(), err)
		}
		if entry.Provider == "" {
			t.Errorf("catalog %s declares no provider key", name.Name())
			continue
		}
		keys[entry.Provider] = name.Name()
	}
	if len(keys) == 0 {
		t.Fatal("read no provider keys from the catalog configs")
	}
	return keys
}

// adapterProviderConstants collects every `Provider = "..."` an adapter
// declares, lowercased, which is the form the catalog keys by.
func adapterProviderConstants(t *testing.T, root string) map[string]string {
	t.Helper()

	constants := make(map[string]string)
	modelsDirectory := filepath.Join(root, "models")
	entries, err := os.ReadDir(modelsDirectory)
	if err != nil {
		t.Fatalf("read models directory: %v", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// A provider may declare its constant in a nested package, as vertexai
		// does under google, so the whole subtree is read.
		root := filepath.Join(modelsDirectory, entry.Name())
		collectProviderConstants(t, root, constants)
	}
	if len(constants) == 0 {
		t.Fatal("read no Provider constants from the adapters")
	}
	return constants
}

func collectProviderConstants(t *testing.T, directory string, into map[string]string) {
	t.Helper()

	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read %s: %v", directory, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			if name == "testdata" || strings.HasPrefix(name, ".") {
				continue
			}
			collectProviderConstants(t, filepath.Join(directory, name), into)
			continue
		}
		if filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(directory, name)
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			// A build-tagged generator is not an adapter surface.
			continue
		}
		for _, declaration := range parsed.Decls {
			generic, ok := declaration.(*ast.GenDecl)
			if !ok || generic.Tok != token.CONST {
				continue
			}
			for _, specification := range generic.Specs {
				value, ok := specification.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for index, identifier := range value.Names {
					if identifier.Name != "Provider" || index >= len(value.Values) {
						continue
					}
					literal, ok := value.Values[index].(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						continue
					}
					unquoted, err := strconv.Unquote(literal.Value)
					if err != nil {
						continue
					}
					into[strings.ToLower(unquoted)] = path
				}
			}
		}
	}
}
