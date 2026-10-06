package arch

import (
	"path/filepath"
	"strings"
	"testing"
)

var targetPublicPackages = map[string]struct{}{
	"chat":                  {},
	"chatclient":            {},
	"chatclient/safeguard":  {},
	"history":               {},
	"history/inmemory":      {},
	"history/storetest":     {},
	"document":              {},
	"embedding":             {},
	"embeddingclient":       {},
	"image":                 {},
	"jsonschema":            {},
	"media":                 {},
	"metadata":              {},
	"modeltest":             {},
	"moderation":            {},
	"rerank":                {},
	"speech":                {},
	"timestamp":             {},
	"tokenizer":             {},
	"tool":                  {},
	"transcription":         {},
	"vectorstore":           {},
	"vectorstore/filter":    {},
	"vectorstore/inmemory":  {},
	"vectorstore/storetest": {},
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
