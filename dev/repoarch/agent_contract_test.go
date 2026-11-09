package repoarch

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestAgentVocabularyNamesOneConcept(t *testing.T) {
	t.Parallel()
	forEachGoFile(t, func(path string, fset *token.FileSet, file *ast.File) {
		if !strings.HasPrefix(filepath.ToSlash(path), "agent/") {
			return
		}
		check := func(position token.Pos, text string) {
			text = strings.ToLower(text)
			for _, retired := range []string{"deployment", "engine"} {
				if strings.Contains(text, retired) {
					t.Errorf("%s: use the canonical Program or Runtime vocabulary", fset.Position(position))
					return
				}
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.Ident:
				check(value.Pos(), value.Name)
			case *ast.BasicLit:
				if value.Kind == token.STRING {
					check(value.Pos(), value.Value)
				}
			}
			return true
		})
		for _, group := range file.Comments {
			check(group.Pos(), group.Text())
		}
	})
}

func TestAgentKernelOwnsItsAtomicContracts(t *testing.T) {
	t.Parallel()
	var storeMethods, messageImplementations []string
	forEachGoFile(t, func(path string, fset *token.FileSet, file *ast.File) {
		path = filepath.ToSlash(path)
		if filepath.Dir(path) != "agent/run" || strings.HasSuffix(path, "_test.go") {
			return
		}
		for _, declaration := range file.Decls {
			switch declaration := declaration.(type) {
			case *ast.FuncDecl:
				if declaration.Recv != nil && declaration.Name.Name == "message" {
					messageImplementations = append(messageImplementations, receiverTypeName(declaration.Recv.List[0].Type))
				}
			case *ast.GenDecl:
				if declaration.Tok != token.TYPE {
					continue
				}
				for _, specification := range declaration.Specs {
					typeSpec := specification.(*ast.TypeSpec)
					if typeSpec.Name.Name == "Store" {
						contract, ok := typeSpec.Type.(*ast.InterfaceType)
						if !ok {
							t.Errorf("%s: Store must expose the append-log contract", fset.Position(typeSpec.Pos()))
							continue
						}
						for _, method := range contract.Methods.List {
							if len(method.Names) != 1 {
								t.Errorf("%s: Store may not hide a second contract through embedding", fset.Position(method.Pos()))
								continue
							}
							storeMethods = append(storeMethods, method.Names[0].Name)
						}
					}
					if typeSpec.Name.Name == "Runtime" {
						assertRuntimeRootRouting(t, fset, typeSpec)
					}
				}
			}
		}
	})
	slices.Sort(storeMethods)
	if !slices.Equal(storeMethods, []string{"Append", "Load", "Read"}) {
		t.Errorf("Store methods = %v; want [Append Load Read]", storeMethods)
	}
	slices.Sort(messageImplementations)
	if !slices.Equal(messageImplementations, []string{"Completion", "Exit", "Signal"}) {
		t.Errorf("Message implementations = %v; want [Completion Exit Signal]", messageImplementations)
	}
}

func assertRuntimeRootRouting(t *testing.T, fset *token.FileSet, declaration *ast.TypeSpec) {
	t.Helper()
	structure, ok := declaration.Type.(*ast.StructType)
	if !ok {
		t.Errorf("%s: Runtime must own root routing", fset.Position(declaration.Pos()))
		return
	}
	// Routing means resolving a tree's owner. A map keyed by root for another
	// purpose, such as claiming a startup, routes nothing.
	var routers []string
	for _, field := range structure.Fields.List {
		mapping, ok := field.Type.(*ast.MapType)
		if !ok {
			continue
		}
		value, ok := mapping.Value.(*ast.StarExpr)
		if !ok {
			continue
		}
		if owner, ok := value.X.(*ast.Ident); !ok || owner.Name != "treeRuntime" {
			continue
		}
		for _, name := range field.Names {
			routers = append(routers, name.Name)
		}
	}
	if !slices.Equal(routers, []string{"roots"}) {
		t.Errorf("%s: Runtime tree routes = %v; only roots may route trees", fset.Position(declaration.Pos()), routers)
	}
}

func TestAgentProgramsHaveOneConstructionBoundary(t *testing.T) {
	t.Parallel()
	strategyPackages := make(map[string]bool)
	forEachGoFile(t, func(path string, _ *token.FileSet, file *ast.File) {
		path = filepath.ToSlash(path)
		if !strings.HasPrefix(path, "agent/") || strings.HasSuffix(path, "_test.go") {
			return
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && function.Recv == nil && function.Name.Name == "Compile" {
				strategyPackages[filepath.Dir(path)] = true
			}
		}
	})
	forEachGoFile(t, func(path string, fset *token.FileSet, file *ast.File) {
		path = filepath.ToSlash(path)
		if !strings.HasPrefix(path, "agent/") || strings.HasSuffix(path, "_test.go") {
			return
		}
		for _, declaration := range file.Decls {
			switch declaration := declaration.(type) {
			case *ast.FuncDecl:
				if declaration.Recv == nil && (declaration.Name.Name == "NewDefinition" || declaration.Name.Name == "NewDispatcher" && strategyPackages[filepath.Dir(path)]) {
					t.Errorf("%s: strategy programs must use their Compile entry point", fset.Position(declaration.Pos()))
				}
			case *ast.GenDecl:
				if declaration.Tok != token.TYPE {
					continue
				}
				for _, specification := range declaration.Specs {
					typeSpec := specification.(*ast.TypeSpec)
					if !ast.IsExported(typeSpec.Name.Name) {
						continue
					}
					if _, ok := typeSpec.Type.(*ast.InterfaceType); ok && (strings.HasSuffix(typeSpec.Name.Name, "Listener") || strings.HasSuffix(typeSpec.Name.Name, "Observer")) {
						t.Errorf("%s: committed observations must come from the log", fset.Position(typeSpec.Pos()))
					}
					if !strings.HasSuffix(typeSpec.Name.Name, "Config") {
						continue
					}
					if structure, ok := typeSpec.Type.(*ast.StructType); ok {
						for _, field := range structure.Fields.List {
							if receiverTypeName(field.Type) == "Digest" {
								t.Errorf("%s: program identity must derive from declared configuration", fset.Position(field.Pos()))
							}
							if selector, ok := field.Type.(*ast.SelectorExpr); ok && selector.Sel.Name == "Digest" {
								t.Errorf("%s: program identity must derive from declared configuration", fset.Position(field.Pos()))
							}
						}
					}
				}
			}
		}
	})
}

func TestAgentKernelDependencyDirection(t *testing.T) {
	t.Parallel()
	const kernel = repositoryModulePrefix + "/agent/run"
	walkProductionGoFiles(t, filepath.Join(repositoryRoot(t), "agent", "run"), func(path string, fset *token.FileSet, file *ast.File) {
		for _, specification := range file.Imports {
			importPath, err := strconv.Unquote(specification.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			shared := importPath == repositoryModulePrefix+"/agent/internal/jsonwire" || importPath == repositoryModulePrefix+"/agent/internal/panicinfo"
			if strings.HasPrefix(importPath, repositoryModulePrefix+"/agent/") && importPath != kernel && !strings.HasPrefix(importPath, kernel+"/") && !shared {
				t.Errorf("%s: the runtime kernel cannot depend on a strategy: %s", fset.Position(specification.Pos()), importPath)
			}
		}
	})
}
