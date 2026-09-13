package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPublicInterfacesAreDocumentedAndParametersNamed(t *testing.T) {
	for _, path := range frameworkProductionGoFiles(t) {
		assertPublicContractPolicyInFile(t, path)
	}
}

func frameworkProductionGoFiles(t *testing.T) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != "." && excludedArchitectureDirectory(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func assertPublicContractPolicyInFile(t *testing.T, path string) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range file.Decls {
		switch declaration := declaration.(type) {
		case *ast.FuncDecl:
			assertExportedFunctionParametersNamed(t, path, declaration)
		case *ast.GenDecl:
			assertExportedInterfaceContracts(t, path, declaration)
		}
	}
}

func assertExportedFunctionParametersNamed(t *testing.T, path string, declaration *ast.FuncDecl) {
	t.Helper()
	publicReceiver := declaration.Recv == nil || token.IsExported(receiverTypeName(declaration.Recv))
	if !declaration.Name.IsExported() || !publicReceiver {
		return
	}
	assertParametersAreNamed(t, path, declaration.Name.Name, declaration.Type.Params)
}

func assertExportedInterfaceContracts(t *testing.T, path string, declaration *ast.GenDecl) {
	t.Helper()
	for _, specification := range declaration.Specs {
		if specification, ok := specification.(*ast.TypeSpec); ok {
			if !specification.Name.IsExported() {
				continue
			}
			assertExportedTypeContract(t, path, declaration.Doc, specification)
		}
	}
}

func assertExportedTypeContract(
	t *testing.T,
	path string,
	declarationDoc *ast.CommentGroup,
	specification *ast.TypeSpec,
) {
	t.Helper()
	switch declaration := specification.Type.(type) {
	case *ast.InterfaceType:
		doc := specification.Doc
		if doc == nil {
			doc = declarationDoc
		}
		assertGoDocStartsWithName(t, path, specification.Name.Name, doc)
		for _, method := range declaration.Methods.List {
			function, ok := method.Type.(*ast.FuncType)
			if !ok {
				continue
			}
			name := specification.Name.Name
			if len(method.Names) > 0 {
				methodName := method.Names[0].Name
				name += "." + methodName
			}
			assertParametersAreNamed(t, path, name, function.Params)
		}
	case *ast.FuncType:
		assertParametersAreNamed(t, path, specification.Name.Name, declaration.Params)
	}
}

func assertParametersAreNamed(t *testing.T, path, callable string, parameters *ast.FieldList) {
	t.Helper()
	if parameters == nil {
		return
	}
	for _, parameter := range parameters.List {
		if len(parameter.Names) == 0 {
			t.Errorf("%s: exported callable %s requires semantically named parameters", path, callable)
		}
	}
}

func assertGoDocStartsWithName(t *testing.T, path, name string, doc *ast.CommentGroup) {
	t.Helper()
	if doc == nil || !strings.HasPrefix(strings.TrimSpace(doc.Text()), name) {
		t.Errorf("%s: exported %s requires GoDoc beginning with its exact name", path, name)
	}
}

func TestProcessHasOneSignalAdmissionContract(t *testing.T) {
	process := reflect.TypeFor[*Process]()
	request := reflect.TypeFor[SignalRequest]()
	requests := reflect.TypeFor[[]SignalRequest]()
	var count int
	for index := range process.NumMethod() {
		method := process.Method(index)
		for parameter := 1; parameter < method.Type.NumIn(); parameter++ {
			input := method.Type.In(parameter)
			if input != request && input != requests {
				continue
			}
			count++
			if method.Name != "DeliverSignals" || input != requests || !method.Type.IsVariadic() {
				t.Errorf("signal admission must use the ordered variadic batch contract: %s", method.Name)
			}
		}
	}
	if count != 1 {
		t.Fatalf("signal admission entrypoints = %d, want 1", count)
	}
}
