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

// Read adapter constants without importing provider dependencies into the catalog.
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

// Model constants recommend catalog entries; retired rows remain only for
// historical attribution and must not be recommended.
func TestModelConstantsNameCatalogedModels(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	catalogs := catalogModelIDs(t, root)

	claimed := make(map[string]bool, len(nonChatModelConstants))
	for _, constant := range adapterModelConstants(t, root) {
		models, covered := catalogs[constant.provider]
		if !covered {
			// Audio, image, and embedding-only backends may have no chat catalog.
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

	for name := range nonChatModelConstants {
		if !claimed[name] {
			t.Errorf("nonChatModelConstants exempts %s, which no adapter declares", name)
		}
	}
}

// The chat catalog excludes these models. Exemptions are explicit so a new
// model constant cannot bypass the catalog through a naming convention.
var nonChatModelConstants = map[string]bool{
	"alibaba.ModelEmbeddingV4":    true,
	"mistral.ModelEmbed":          true,
	"mistral.ModelCodestralEmbed": true,
	"mistral.ModelModeration":     true,
	"zhipu.ModelEmbedding3":       true,
}

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
		// Provider constants may live in nested packages, such as google/vertexai.
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
