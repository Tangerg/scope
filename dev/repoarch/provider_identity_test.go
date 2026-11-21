package repoarch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var providerSlug = regexp.MustCompile(`^[a-z0-9]+$`)

// Every adapter family exports Provider as the stable backend name hosts
// attribute work to, so models, vector stores and retrievers must share one
// spelling: the lowercase slug the catalog keys providers by.
func TestProviderConstantsAreLowercaseSlugs(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	for _, family := range []string{"models", "vectorstores", "retrievers"} {
		err := filepath.WalkDir(filepath.Join(root, family), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			if value, found := providerLiteral(file); found && !providerSlug.MatchString(value) {
				relative, _ := filepath.Rel(root, path)
				t.Errorf("%s declares Provider %q; want a lowercase slug", filepath.ToSlash(relative), value)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func providerLiteral(file *ast.File) (string, bool) {
	for _, declaration := range file.Decls {
		generic, ok := declaration.(*ast.GenDecl)
		if !ok || generic.Tok != token.CONST {
			continue
		}
		for _, specification := range generic.Specs {
			value := specification.(*ast.ValueSpec)
			for index, identifier := range value.Names {
				if identifier.Name != "Provider" || index >= len(value.Values) {
					continue
				}
				literal, ok := value.Values[index].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					return "", false
				}
				unquoted, err := strconv.Unquote(literal.Value)
				return unquoted, err == nil
			}
		}
	}
	return "", false
}
