// Package providerconformance_test locks cross-provider constructor contracts.
package providerconformance_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestProviderConstructorsAreSelfCovering(t *testing.T) {
	providers := modelProviderDirectories(t)
	if len(providers) == 0 {
		t.Fatal("no model provider directories discovered")
	}
	constructors := 0
	for _, provider := range providers {
		declarations, validateReceivers := parseProviderDeclarations(t, provider)
		for _, function := range declarations {
			if validateProviderConstructor(t, function, validateReceivers) {
				constructors++
			}
		}
	}
	if constructors == 0 {
		t.Fatal("no provider constructors discovered")
	}
}

func modelProviderDirectories(t *testing.T) []string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve provider conformance source path")
	}
	modelsRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", "models"))
	var directories []string
	err := filepath.WalkDir(modelsRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		if entry.Name() == "internal" || entry.Name() == "catalog" {
			return filepath.SkipDir
		}
		files, err := filepath.Glob(filepath.Join(path, "*.go"))
		if err != nil {
			return err
		}
		if len(files) > 0 {
			directories = append(directories, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return directories
}

func parseProviderDeclarations(t *testing.T, provider string) ([]*ast.FuncDecl, map[string]struct{}) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(provider, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	validateReceivers := map[string]struct{}{}
	var declarations []*ast.FuncDecl
	fileSet := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fileSet, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok {
				continue
			}
			declarations = append(declarations, function)
			addValidateReceiver(validateReceivers, function)
		}
	}
	return declarations, validateReceivers
}

func addValidateReceiver(receivers map[string]struct{}, function *ast.FuncDecl) {
	if function.Name.Name != "Validate" || function.Recv == nil || len(function.Recv.List) != 1 {
		return
	}
	if receiver := namedType(function.Recv.List[0].Type); receiver != "" {
		receivers[receiver] = struct{}{}
	}
}

func validateProviderConstructor(
	t *testing.T,
	function *ast.FuncDecl,
	validateReceivers map[string]struct{},
) bool {
	t.Helper()
	if function.Recv != nil || !strings.HasPrefix(function.Name.Name, "New") || !function.Name.IsExported() {
		return false
	}
	configType, parameterCount := constructorConfig(function.Type.Params)
	if configType == "" {
		return false
	}
	if parameterCount != 2 && (parameterCount != 3 || namedType(function.Type.Params.List[len(function.Type.Params.List)-1].Type) != "Dialect") {
		t.Errorf("%s: constructor with config has %d parameters", function.Name.Name, parameterCount)
	}
	if !startsWithContext(function.Type.Params) {
		t.Errorf("%s: only context.Context may precede config", function.Name.Name)
	}
	if _, ok := validateReceivers[configType]; !ok {
		t.Errorf("%s: %s does not own Validate", function.Name.Name, configType)
	}
	if !returnsValueAndError(function.Type.Results) {
		t.Errorf("%s: constructor must return value and error", function.Name.Name)
	}
	return true
}

func constructorConfig(parameters *ast.FieldList) (string, int) {
	if parameters == nil {
		return "", 0
	}
	count := 0
	configType := ""
	for _, field := range parameters.List {
		fieldCount := max(1, len(field.Names))
		count += fieldCount
		if name := namedType(field.Type); strings.HasSuffix(name, "Config") {
			configType = name
		}
	}
	return configType, count
}

func namedType(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.StarExpr:
		return namedType(value.X)
	default:
		return ""
	}
}

func startsWithContext(parameters *ast.FieldList) bool {
	if parameters == nil || len(parameters.List) == 0 {
		return false
	}
	selector, ok := parameters.List[0].Type.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Context" {
		return false
	}
	packageName, ok := selector.X.(*ast.Ident)
	return ok && packageName.Name == "context"
}

func returnsValueAndError(results *ast.FieldList) bool {
	if results == nil || len(results.List) != 2 {
		return false
	}
	errorType, ok := results.List[1].Type.(*ast.Ident)
	return ok && errorType.Name == "error"
}

func TestProviderDiscoveryIncludesNestedPublicProtocols(t *testing.T) {
	directories := modelProviderDirectories(t)
	for _, suffix := range []string{"google/vertexai", "protocol/openai", "protocol/anthropic"} {
		found := false
		for _, directory := range directories {
			if strings.HasSuffix(filepath.ToSlash(directory), "/"+suffix) {
				found = true
			}
		}
		if !found {
			t.Errorf("provider discovery omitted %s", suffix)
		}
	}
}

func TestConstructorContextIsMandatory(t *testing.T) {
	for _, test := range []struct {
		signature string
		valid     bool
	}{
		{"func New(config Config) (*Model, error) { return nil,nil }", false},
		{"func New(config Config, ctx context.Context) (*Model, error) { return nil,nil }", false},
		{"func New(ctx context.Context, config Config) (*Model, error) { return nil,nil }", true},
	} {
		parsed, err := parser.ParseFile(token.NewFileSet(), "constructor.go", "package provider\n"+test.signature, 0)
		if err != nil {
			t.Fatal(err)
		}
		function := parsed.Decls[0].(*ast.FuncDecl)
		if got := startsWithContext(function.Type.Params); got != test.valid {
			t.Errorf("context check = %t for %s", got, test.signature)
		}
	}
}
