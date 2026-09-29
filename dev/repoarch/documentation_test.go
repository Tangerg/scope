package repoarch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var documentedCapabilityModules = []string{
	"a2a",
	"agent",
	"etl",
	"eval",
	"mcp",
	"otel",
	"rag",
	"skills",
	"tools",
}

var documentationOnlyModuleRoots = []string{"core", "examples", "otel", "tools"}

type moduleDocumentation struct {
	packageOverviews map[string]bool
	checkedExamples  int
}

func TestWorkspaceModulesKeepDocumentationEntryPoints(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	modules := discoverModules(t, root)
	for _, modulePath := range slices.Sorted(maps.Keys(modules)) {
		module := modules[modulePath]
		t.Run(module.dir, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(root, filepath.FromSlash(module.dir), "doc.go")
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("module %s is missing doc.go: %v", module.path, err)
			}
			if !info.Mode().IsRegular() || info.Size() == 0 {
				t.Fatalf("module %s has no readable content in doc.go", module.path)
			}
			assertPackageOverview(t, path)
			for _, retired := range []string{"README.md", "ARCHITECTURE.md"} {
				retiredPath := filepath.Join(root, filepath.FromSlash(module.dir), retired)
				if _, err := os.Stat(retiredPath); !os.IsNotExist(err) {
					t.Errorf("module %s keeps retired parallel documentation %s", module.path, retired)
				}
			}
		})
	}
}

func TestNamespaceDirectoriesKeepOneDocumentationEntry(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	namespaces := discoverNamespaceDirectories(t, root)
	if len(namespaces) == 0 {
		t.Fatal("discovered no namespace directories; the gate would pass vacuously")
	}
	for _, namespace := range namespaces {
		t.Run(namespace, func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(root, filepath.FromSlash(namespace))
			info, err := os.Stat(filepath.Join(dir, "README.md"))
			if err != nil {
				t.Fatalf("namespace %s is missing README.md: %v", namespace, err)
			}
			if !info.Mode().IsRegular() || info.Size() == 0 {
				t.Fatalf("namespace %s has no readable content in README.md", namespace)
			}
			for _, retired := range []string{"ARCHITECTURE.md", "doc.go"} {
				if _, err := os.Stat(filepath.Join(dir, retired)); !os.IsNotExist(err) {
					t.Errorf("namespace %s keeps a second documentation entry %s", namespace, retired)
				}
			}
		})
	}
}

func discoverNamespaceDirectories(t *testing.T, root string) []string {
	t.Helper()
	namespaces := make(map[string]struct{})
	for _, module := range discoverModules(t, root) {
		prefix, _, nested := strings.Cut(filepath.ToSlash(module.dir), "/")
		if !nested {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, prefix, "go.mod")); os.IsNotExist(err) {
			namespaces[prefix] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(namespaces))
}

func TestRepositoryGuidanceHasOneCanonicalSource(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	claudePath := filepath.Join(root, "CLAUDE.md")
	claude, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(claude), "@./AGENTS.md") != 1 {
		t.Error("CLAUDE.md must point to the canonical AGENTS.md exactly once")
	}

	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relativePath, relativeErr := filepath.Rel(root, path)
		if relativeErr != nil {
			return relativeErr
		}
		if entry.IsDir() {
			if shouldSkipRepositoryDir(filepath.ToSlash(relativePath), entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if relativePath == "AGENTS.md" || relativePath == "CLAUDE.md" {
			return nil
		}
		if entry.Name() == "AGENTS.md" || entry.Name() == "CLAUDE.md" {
			t.Errorf("%s duplicates repository guidance; put package contracts in GoDoc", filepath.ToSlash(relativePath))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRootReadmeKeepsDirectChatPath(t *testing.T) {
	t.Parallel()
	readme, err := os.ReadFile(filepath.Join(repositoryRoot(t), "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(readme)
	for _, required := range []string{
		"```go",
		"github.com/Tangerg/scope/core/chatclient",
		"chatclient.New(",
		"client.Call(",
	} {
		if !strings.Contains(content, required) {
			t.Errorf("README.md is missing direct chat entry %q", required)
		}
	}
}

func TestWorkspaceModulesDocumentPublishedPackages(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	modules := discoverModules(t, root)
	for _, modulePath := range slices.Sorted(maps.Keys(modules)) {
		module := modules[modulePath]
		t.Run(module.dir, func(t *testing.T) {
			t.Parallel()
			moduleRoot := filepath.Join(root, filepath.FromSlash(module.dir))
			documentation := inspectModuleDocumentation(t, moduleRoot)
			for directory, documented := range documentation.packageOverviews {
				if !documented {
					t.Errorf("public package %s has no Package comment in production code", filepath.ToSlash(directory))
				}
			}
		})
	}
}

func TestDocumentationOnlyModuleRootsStayDocumentationOnly(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	for _, relative := range documentationOnlyModuleRoots {
		t.Run(relative, func(t *testing.T) {
			t.Parallel()
			directory := filepath.Join(root, relative)
			entries, err := os.ReadDir(directory)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
					continue
				}
				if entry.Name() != "doc.go" {
					t.Errorf("documentation-only module root %s contains production file %s", relative, entry.Name())
				}
			}

			path := filepath.Join(directory, "doc.go")
			file := assertPackageOverview(t, path)
			if len(file.Decls) != 0 {
				t.Errorf("%s/doc.go declares production API or implementation", relative)
			}
		})
	}
}

func assertPackageOverview(t *testing.T, path string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	if !hasPackageOverview(file) {
		t.Errorf("%s has no package overview", path)
	}
	return file
}

func hasPackageOverview(file *ast.File) bool {
	return file.Doc != nil && strings.HasPrefix(strings.TrimSpace(file.Doc.Text()), "Package "+file.Name.Name)
}

func TestCapabilityModulesKeepCheckedExamples(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	for _, relativeModule := range documentedCapabilityModules {
		t.Run(relativeModule, func(t *testing.T) {
			t.Parallel()
			moduleRoot := filepath.Join(root, filepath.FromSlash(relativeModule))
			documentation := inspectModuleDocumentation(t, moduleRoot)
			if len(documentation.packageOverviews) == 0 {
				t.Fatalf("capability module %s has no public packages", relativeModule)
			}
			if documentation.checkedExamples == 0 {
				t.Errorf("capability module %s has no checked Go usage example", relativeModule)
			}
		})
	}
}

func inspectModuleDocumentation(t *testing.T, moduleRoot string) moduleDocumentation {
	t.Helper()
	documentation := moduleDocumentation{packageOverviews: make(map[string]bool)}
	err := filepath.WalkDir(moduleRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != moduleRoot && (excludedDocumentationDirectory(entry.Name()) || isModuleDirectory(path)) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		if strings.HasSuffix(entry.Name(), "_test.go") {
			documentation.checkedExamples += countCheckedExamples(file)
			return nil
		}
		if file.Name.Name == "main" {
			return nil
		}
		directory := filepath.Dir(path)
		documentation.packageOverviews[directory] = documentation.packageOverviews[directory] || hasPackageOverview(file)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return documentation
}

// A nested module documents and exemplifies itself, so its files must not
// satisfy the enclosing module's gates.
func isModuleDirectory(path string) bool {
	_, err := os.Stat(filepath.Join(path, "go.mod"))
	return err == nil
}

func excludedDocumentationDirectory(name string) bool {
	return name == "examples" || name == "internal" || name == "testdata" || strings.HasPrefix(name, ".")
}

func countCheckedExamples(file *ast.File) int {
	count := 0
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil || !strings.HasPrefix(function.Name.Name, "Example") {
			continue
		}
		for _, comment := range file.Comments {
			if comment.Pos() < function.Body.Pos() || comment.End() > function.Body.End() {
				continue
			}
			text := strings.TrimSpace(comment.Text())
			if strings.HasPrefix(text, "Output:") || strings.HasPrefix(text, "Unordered output:") {
				count++
				break
			}
		}
	}
	return count
}
