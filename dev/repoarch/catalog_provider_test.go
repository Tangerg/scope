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
	providers := make(map[string]bool)
	for _, adapter := range adapterPackages(t, root) {
		providers[adapter.provider] = true
	}

	for provider, catalog := range catalogConfigs(t, root) {
		if !providers[provider] {
			t.Errorf("catalog %s keys provider %q, which no adapter declares as its Provider constant", catalog.file, provider)
		}
	}
}

// Model constants recommend catalog entries; retired rows remain only for
// historical attribution and must not be recommended.
func TestModelConstantsNameCatalogedModels(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	catalogs := catalogConfigs(t, root)

	claimed := make(map[string]bool, len(nonChatModelConstants))
	for _, adapter := range adapterPackages(t, root) {
		catalog, covered := catalogs[adapter.provider]
		if !covered {
			// Audio, image, and embedding-only backends may have no chat catalog.
			continue
		}
		for _, constant := range adapter.models {
			if nonChatModelConstants[constant.qualifiedName()] {
				claimed[constant.qualifiedName()] = true
				continue
			}
			deprecated, found := catalog.models[constant.id]
			switch {
			case !found:
				t.Errorf("%s:%d: %s names %q, which the %s catalog does not carry",
					constant.file, constant.line, constant.qualifiedName(), constant.id, adapter.provider)
			case deprecated:
				t.Errorf("%s:%d: %s names %q, which the %s catalog marks deprecated",
					constant.file, constant.line, constant.qualifiedName(), constant.id, adapter.provider)
			}
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

type catalogConfig struct {
	file   string
	models map[string]bool
}

type adapterPackage struct {
	provider string
	models   []modelConstant
}

type modelConstant struct {
	pkg  string
	name string
	id   string
	file string
	line int
}

func (m modelConstant) qualifiedName() string {
	return m.pkg + "." + m.name
}

func catalogConfigs(t *testing.T, root string) map[string]catalogConfig {
	t.Helper()

	directory := filepath.Join(root, "models", "catalog", "configs")
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read catalog configs: %v", err)
	}
	catalogs := make(map[string]catalogConfig, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		var config struct {
			Provider string `json:"provider"`
			Models   []struct {
				ID         string `json:"id"`
				Deprecated bool   `json:"deprecated"`
			} `json:"models"`
		}
		if err := jsonv2.Unmarshal(raw, &config); err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
		if config.Provider == "" {
			t.Errorf("catalog %s declares no provider key", name)
			continue
		}
		if previous, duplicate := catalogs[config.Provider]; duplicate {
			t.Errorf("catalogs %s and %s both key provider %q", previous.file, name, config.Provider)
			continue
		}
		models := make(map[string]bool, len(config.Models))
		for _, model := range config.Models {
			models[model.ID] = model.Deprecated
		}
		catalogs[config.Provider] = catalogConfig{file: name, models: models}
	}
	if len(catalogs) == 0 {
		t.Fatal("read no provider keys from the catalog configs")
	}
	return catalogs
}

func adapterPackages(t *testing.T, root string) []adapterPackage {
	t.Helper()

	var adapters []adapterPackage
	collectAdapterPackages(t, filepath.Join(root, "models"), &adapters)
	if len(adapters) == 0 {
		t.Fatal("read no Provider constants from the adapters")
	}
	return adapters
}

// A package declares an adapter surface through its Provider constant; the
// Model constants of a package without one belong to no catalog.
func collectAdapterPackages(t *testing.T, directory string, into *[]adapterPackage) {
	t.Helper()

	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read %s: %v", directory, err)
	}
	fileSet := token.NewFileSet()
	var adapter adapterPackage
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			if name != "testdata" && !strings.HasPrefix(name, ".") {
				collectAdapterPackages(t, filepath.Join(directory, name), into)
			}
			continue
		}
		if filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(directory, name)
		parsed, err := parser.ParseFile(fileSet, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for identifier, literal := range stringConstants(t, path, parsed) {
			switch {
			case identifier.Name == "Provider":
				adapter.provider = strings.ToLower(literal)
			case strings.HasPrefix(identifier.Name, "Model") && identifier.IsExported():
				adapter.models = append(adapter.models, modelConstant{
					pkg:  parsed.Name.Name,
					name: identifier.Name,
					id:   literal,
					file: filepath.ToSlash(path),
					line: fileSet.Position(identifier.Pos()).Line,
				})
			}
		}
	}
	if adapter.provider != "" {
		*into = append(*into, adapter)
	}
}

func stringConstants(t *testing.T, path string, file *ast.File) func(func(*ast.Ident, string) bool) {
	return func(yield func(*ast.Ident, string) bool) {
		for _, declaration := range file.Decls {
			generic, ok := declaration.(*ast.GenDecl)
			if !ok || generic.Tok != token.CONST {
				continue
			}
			for _, specification := range generic.Specs {
				value := specification.(*ast.ValueSpec)
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
						t.Fatalf("%s: constant %s: %v", path, identifier.Name, err)
					}
					if !yield(identifier, unquoted) {
						return
					}
				}
			}
		}
	}
}
