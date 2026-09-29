package providerconformance_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The web suites enumerate providers by hand, so a provider that asserts a web
// contract without joining its suite would otherwise escape validation.
func TestWebSuitesCoverEveryAssertedContract(t *testing.T) {
	root := filepath.Join(repositoryRoot(t), "tools", "web")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	suites := map[string]map[string]bool{"Searcher": {}, "Fetcher": {}}
	for name := range webSearchers {
		suites["Searcher"][name] = true
	}
	for name := range webFetchers {
		suites["Fetcher"][name] = true
	}
	providers := 0
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "internal" {
			continue
		}
		contracts := assertedWebContracts(t, filepath.Join(root, entry.Name()))
		if len(contracts) == 0 {
			continue
		}
		providers++
		for contract := range contracts {
			covered, known := suites[contract]
			if !known {
				t.Errorf("%s asserts web.%s, which no conformance suite checks", entry.Name(), contract)
				continue
			}
			if !covered[entry.Name()] {
				t.Errorf("%s asserts web.%s but is missing from its conformance suite", entry.Name(), contract)
			}
		}
	}
	if providers == 0 {
		t.Fatal("discovered no web providers")
	}
}

func assertedWebContracts(t *testing.T, directory string) map[string]bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(directory, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	contracts := make(map[string]bool)
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, declaration := range file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.VAR {
				continue
			}
			for _, specification := range general.Specs {
				value := specification.(*ast.ValueSpec)
				selector, ok := value.Type.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				if qualifier, ok := selector.X.(*ast.Ident); ok && qualifier.Name == "web" {
					contracts[selector.Sel.Name] = true
				}
			}
		}
	}
	return contracts
}
