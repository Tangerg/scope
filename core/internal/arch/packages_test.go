package arch

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

var targetPublicPackages = map[string]struct{}{
	"content":                    {},
	"chat":                       {},
	"chatclient":                 {},
	"chatclient/safeguard":       {},
	"history":                    {},
	"history/inmemory":           {},
	"history/storetest":          {},
	"httpclient":                 {},
	"document":                   {},
	"embedding":                  {},
	"embeddingclient":            {},
	"image":                      {},
	"jsonschema":                 {},
	"media":                      {},
	"metadata":                   {},
	"modeltest":                  {},
	"moderation":                 {},
	"rerank":                     {},
	"speech":                     {},
	"timestamp":                  {},
	"tokenizer":                  {},
	"tool":                       {},
	"transcription":              {},
	"vectorstore":                {},
	"vectorstore/filter/keyword": {},
	"vectorstore/filter":         {},
	"vectorstore/inmemory":       {},
	"vectorstore/storetest":      {},
}

func TestPublicPackagesMatchArchitectureAllowlist(t *testing.T) {
	root := coreRoot(t)
	seen := make(map[string]struct{})
	for _, path := range productionGoFiles(t) {
		dir := filepath.Dir(path)
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			t.Fatalf("relative package path for %s: %v", path, err)
		}
		rel = filepath.ToSlash(rel)
		if rel == "." || strings.Contains("/"+rel+"/", "/internal/") {
			continue
		}
		seen[rel] = struct{}{}
	}

	for packagePath := range seen {
		if _, ok := targetPublicPackages[packagePath]; ok {
			continue
		}
		t.Errorf("public package %q is outside the target architecture", packagePath)
	}
}

func TestToolProtocolDoesNotDependOnChat(t *testing.T) {
	root := coreRoot(t)
	for _, filename := range productionGoFiles(t) {
		if filepath.Dir(filename) != filepath.Join(root, "tool") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filename, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, dep := range file.Imports {
			if dep.Path.Value == `"github.com/Tangerg/scope/core/chat"` {
				t.Errorf("%s: tools must execute without a model protocol", filename)
			}
		}
	}
}
