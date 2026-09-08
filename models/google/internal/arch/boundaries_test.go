// Package arch_test locks the Google integration module's dependency and
// provider-facing API boundaries.
package arch_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/Tangerg/scope/models/google"
	"github.com/Tangerg/scope/models/google/vertexai"
)

const modulePath = "github.com/Tangerg/scope/models/google"

type configValidator interface {
	Validate() error
}

func TestProviderConstructorsCompile(t *testing.T) {
	t.Parallel()

	// Both constructors take a context because construction reaches the
	// network: on the Vertex AI backend with application default credentials,
	// building the client resolves them and then asks the resolved credential
	// for its quota project over the metadata server.
	var (
		_ func(context.Context, google.ChatConfig) (*google.Chat, error)     = google.NewChat
		_ func(context.Context, vertexai.ChatConfig) (*vertexai.Chat, error) = vertexai.NewChat
	)
}

func TestProviderConfigsValidate(t *testing.T) {
	t.Parallel()
	var (
		_ configValidator = google.ChatConfig{}
		_ configValidator = vertexai.ChatConfig{}
	)
}

func TestDependenciesAreOneWay(t *testing.T) {
	t.Parallel()

	root := moduleRoot(t)
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(filename, ".go") || strings.HasSuffix(filename, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, filename)
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.ToSlash(relative), "/")
		source := "google"
		if len(parts) > 1 {
			source = parts[0]
		}
		file, err := parser.ParseFile(fset, filename, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imported := range file.Imports {
			pathValue, err := strconv.Unquote(imported.Path.Value)
			if err != nil || (pathValue != modulePath && !strings.HasPrefix(pathValue, modulePath+"/")) {
				continue
			}
			target := strings.TrimPrefix(pathValue, modulePath)
			target = strings.TrimPrefix(target, "/")
			targetRoot := "google"
			if target != "" {
				targetRoot = strings.Split(target, "/")[0]
			}
			if source == "internal" && targetRoot != "internal" {
				t.Errorf("%s:%d internal protocol must not depend on provider %q", filepath.ToSlash(relative), fset.Position(imported.Pos()).Line, targetRoot)
			}
			if source != "internal" && targetRoot != "internal" && targetRoot != source {
				t.Errorf("%s:%d provider %q must not depend on peer provider %q", filepath.ToSlash(relative), fset.Position(imported.Pos()).Line, source, targetRoot)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestProviderAPIsHideProtocolTypes(t *testing.T) {
	t.Parallel()

	root := moduleRoot(t)
	files, err := filepath.Glob(filepath.Join(root, "*", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, rootGoFiles(t, root)...)
	fset := token.NewFileSet()
	for _, filename := range files {
		if strings.HasSuffix(filename, "_test.go") || strings.Contains(filepath.ToSlash(filename), "/internal/") {
			continue
		}
		file, err := parser.ParseFile(fset, filename, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		aliases := protocolAliases(file)
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || !function.Name.IsExported() {
				continue
			}
			ast.Inspect(function.Type, func(node ast.Node) bool {
				selector, ok := node.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				qualifier, ok := selector.X.(*ast.Ident)
				if ok {
					if _, protocol := aliases[qualifier.Name]; protocol {
						t.Errorf("%s:%d exported API leaks protocol type %s.%s", filepath.Base(filename), fset.Position(selector.Pos()).Line, qualifier.Name, selector.Sel.Name)
					}
				}
				return true
			})
		}
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate architecture test")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
}

func rootGoFiles(t *testing.T, root string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(root, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func protocolAliases(file *ast.File) map[string]struct{} {
	aliases := make(map[string]struct{})
	for _, imported := range file.Imports {
		pathValue, err := strconv.Unquote(imported.Path.Value)
		if err != nil || (!strings.Contains(pathValue, "/internal/protocol/") && !strings.Contains(pathValue, "/models/protocol/")) {
			continue
		}
		name := filepath.Base(pathValue)
		if imported.Name != nil {
			name = imported.Name.Name
		}
		aliases[name] = struct{}{}
	}
	return aliases
}
