package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

var frameworkPackageDirectories = []string{
	".", "agenttest", "interaction", "planning", "planning/goap", "workflow",
}

func TestPublicInterfacesAreDocumentedAndParametersNamed(t *testing.T) {
	for _, path := range frameworkProductionGoFiles(t) {
		assertPublicContractPolicyInFile(t, path)
	}
}

func TestManagedExecutionVocabularyIsUnambiguous(t *testing.T) {
	paths := frameworkProductionGoFiles(t)
	forbidden := []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bna` + `tive\b`),
		regexp.MustCompile(regexp.QuoteMeta("原" + "生")),
		regexp.MustCompile(`\bStep trans` + `actions?\b`),
		regexp.MustCompile(regexp.QuoteMeta("候选" + "事务")),
	}
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, pattern := range forbidden {
			if pattern.Match(content) {
				t.Errorf("%s uses retired managed-execution terminology matching %q", path, pattern)
			}
		}
	}
}

func frameworkProductionGoFiles(t *testing.T) []string {
	t.Helper()
	var paths []string
	for _, directory := range frameworkPackageDirectories {
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if !entry.IsDir() && strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
				paths = append(paths, filepath.Join(directory, name))
			}
		}
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
				assertGoDocStartsWithName(t, path, methodName, method.Doc)
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

func TestErrorCausesAreWrapped(t *testing.T) {
	for _, path := range frameworkProductionGoFiles(t) {
		assertErrorCausesWrappedInFile(t, path)
	}
}

func assertErrorCausesWrappedInFile(t *testing.T, path string) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 || !isFmtErrorf(call.Fun) {
			return true
		}
		format, ok := stringLiteral(call.Args[0])
		if !ok || !strings.Contains(format, "%v") {
			return true
		}
		assertErrorArgumentsWrapped(t, path, call.Args[1:])
		return true
	})
}

func assertErrorArgumentsWrapped(t *testing.T, path string, arguments []ast.Expr) {
	t.Helper()
	for _, argument := range arguments {
		identifier, ok := argument.(*ast.Ident)
		if ok && isErrorCauseName(identifier.Name) {
			t.Errorf(
				"%s: fmt.Errorf formats cause %s with %%v; preserve errors.Is/As with %%w",
				path, identifier.Name,
			)
		}
	}
}

func isFmtErrorf(expression ast.Expr) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Errorf" {
		return false
	}
	packageName, ok := selector.X.(*ast.Ident)
	return ok && packageName.Name == "fmt"
}

func stringLiteral(expression ast.Expr) (string, bool) {
	literal, ok := expression.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	return strings.Trim(literal.Value, "`\""), true
}

func isErrorCauseName(name string) bool {
	return name == "err" || name == "cause" ||
		strings.HasSuffix(name, "Err") || strings.HasSuffix(name, "Error")
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
