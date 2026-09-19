package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Unlimited defaults cannot document whether a finite zero disables a capability
// or makes construction invalid. Require that decision beside each public grant.
func TestQuotaConfigurationDeclaresFiniteZeroPolicy(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		agentImport := ""
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			if importPath == "github.com/Tangerg/scope/agent" {
				agentImport = "agent"
				if spec.Name != nil {
					agentImport = spec.Name.Name
				}
			}
		}
		for _, declaration := range file.Decls {
			group, ok := declaration.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range group.Specs {
				named, ok := spec.(*ast.TypeSpec)
				if !ok || !named.Name.IsExported() {
					continue
				}
				name := named.Name.Name
				if !strings.HasSuffix(name, "Config") && name != "Limits" && name != "TreeLimits" && name != "Budget" {
					continue
				}
				structure, ok := named.Type.(*ast.StructType)
				if !ok {
					continue
				}
				for _, field := range structure.Fields.List {
					quota := false
					switch fieldType := field.Type.(type) {
					case *ast.Ident:
						quota = file.Name.Name == "agent" && fieldType.Name == "Quota"
					case *ast.SelectorExpr:
						qualifier, ok := fieldType.X.(*ast.Ident)
						quota = ok && qualifier.Name == agentImport && fieldType.Sel.Name == "Quota"
					}
					if !quota {
						continue
					}
					policy := strings.ToLower(strings.Join(strings.Fields(field.Doc.Text()), " "))
					for _, fieldName := range field.Names {
						if fieldName.IsExported() && !strings.Contains(policy, "finite zero") {
							t.Errorf("%s: %s.%s must declare its finite zero policy", path, name, fieldName.Name)
						}
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
