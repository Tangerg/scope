package repoarch

import (
	jsonv2 "encoding/json/v2"
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

// The catalog owns which models exist. A provider's Model constants are a
// curated selection from it, never a second source for it: taking a constant
// and asking the catalog about it is the intended use, so a constant the
// catalog cannot answer for is a contradiction a caller meets at runtime.
//
// Without this check the two drift silently, because upstream retiring a model
// updates the generated catalog and leaves the hand-kept constant behind.
//
// A deprecated row stays in the catalog only so cost still attributes for
// callers already on that id, which is the opposite of a recommendation, so a
// constant must not name one either.
func TestModelConstantsNameCatalogedModels(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	catalogs := catalogModelIDs(t, root)

	claimed := make(map[string]bool, len(nonChatModelConstants))
	for _, constant := range adapterModelConstants(t, root) {
		models, covered := catalogs[constant.provider]
		if !covered {
			// The provider has no catalog config at all: an audio, image, or
			// embedding-only backend the chat catalog does not describe.
			continue
		}
		if nonChatModelConstants[constant.qualifiedName()] {
			claimed[constant.qualifiedName()] = true
			continue
		}
		deprecated, found := models[constant.id]
		switch {
		case !found:
			t.Errorf("%s:%d: %s names %q, which the %s catalog does not carry",
				constant.file, constant.line, constant.qualifiedName(), constant.id, constant.provider)
		case deprecated:
			t.Errorf("%s:%d: %s names %q, which the %s catalog marks deprecated",
				constant.file, constant.line, constant.qualifiedName(), constant.id, constant.provider)
		}
	}

	// An exemption whose constant is gone is indistinguishable from a typo, and
	// it silently widens the next one that happens to be named the same.
	for name := range nonChatModelConstants {
		if !claimed[name] {
			t.Errorf("nonChatModelConstants exempts %s, which no adapter declares", name)
		}
	}
}

// nonChatModelConstants are the constants that name a model the catalog does
// not carry by design: it is a chat catalog, and an embedding or moderation
// model is filtered out of it at generation.
//
// The exemption is a list rather than a naming convention so that adding one
// is a decision someone writes down. Anything not listed has to be in the
// catalog.
var nonChatModelConstants = map[string]bool{
	"alibaba.ModelEmbeddingV4":    true,
	"mistral.ModelEmbed":          true,
	"mistral.ModelCodestralEmbed": true,
	"mistral.ModelModeration":     true,
	"zhipu.ModelEmbedding3":       true,
}

// modelConstant is one exported model id constant, tied to the provider its
// package declares.
type modelConstant struct {
	provider string
	pkg      string
	name     string
	id       string
	file     string
	line     int
}

func (m modelConstant) qualifiedName() string {
	return m.pkg + "." + m.name
}

// catalogModelIDs reads every embedded config, returning
// provider key -> model id -> deprecated.
func catalogModelIDs(t *testing.T, root string) map[string]map[string]bool {
	t.Helper()

	directory := filepath.Join(root, "models", "catalog", "configs")
	names, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read catalog configs: %v", err)
	}
	catalogs := make(map[string]map[string]bool, len(names))
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
			Models   []struct {
				ID         string `json:"id"`
				Deprecated bool   `json:"deprecated"`
			} `json:"models"`
		}
		if err := jsonv2.Unmarshal(raw, &entry); err != nil {
			t.Fatalf("decode %s: %v", name.Name(), err)
		}
		models := make(map[string]bool, len(entry.Models))
		for _, model := range entry.Models {
			models[model.ID] = model.Deprecated
		}
		catalogs[entry.Provider] = models
	}
	if len(catalogs) == 0 {
		t.Fatal("read no models from the catalog configs")
	}
	return catalogs
}

// adapterModelConstants collects every exported Model* constant, tied to the
// Provider constant its own package declares.
func adapterModelConstants(t *testing.T, root string) []modelConstant {
	t.Helper()

	var constants []modelConstant
	collectModelConstants(t, filepath.Join(root, "models"), &constants)
	if len(constants) == 0 {
		t.Fatal("read no model id constants from the adapters")
	}
	return constants
}

func collectModelConstants(t *testing.T, directory string, into *[]modelConstant) {
	t.Helper()

	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read %s: %v", directory, err)
	}
	fileSet := token.NewFileSet()
	provider := ""
	packageName := ""
	var candidates []modelConstant
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			if name == "testdata" || strings.HasPrefix(name, ".") {
				continue
			}
			collectModelConstants(t, filepath.Join(directory, name), into)
			continue
		}
		if filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(directory, name)
		parsed, err := parser.ParseFile(fileSet, path, nil, 0)
		if err != nil {
			// A build-tagged generator is not an adapter surface.
			continue
		}
		for identifier, literal := range stringConstants(parsed) {
			switch {
			case identifier.Name == "Provider":
				provider = strings.ToLower(literal)
				packageName = parsed.Name.Name
			case strings.HasPrefix(identifier.Name, "Model") && identifier.IsExported():
				position := fileSet.Position(identifier.Pos())
				candidates = append(candidates, modelConstant{
					name: identifier.Name,
					id:   literal,
					file: filepath.ToSlash(path),
					line: position.Line,
				})
			}
		}
	}
	// A package without a Provider constant declares no adapter surface, so
	// its constants belong to no catalog.
	if provider == "" {
		return
	}
	for _, candidate := range candidates {
		candidate.provider = provider
		candidate.pkg = packageName
		*into = append(*into, candidate)
	}
}

// stringConstants yields every `Name = "literal"` declared at the top level.
func stringConstants(file *ast.File) func(func(*ast.Ident, string) bool) {
	return func(yield func(*ast.Ident, string) bool) {
		for _, declaration := range file.Decls {
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
					if index >= len(value.Values) {
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
					if !yield(identifier, unquoted) {
						return
					}
				}
			}
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
		if err := jsonv2.Unmarshal(raw, &entry); err != nil {
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
