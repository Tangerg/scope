package arch_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestExecutableToolProtocolStaysMinimal(t *testing.T) {
	typeOf := reflect.TypeFor[tool.Tool]()
	if typeOf.Kind() != reflect.Interface || typeOf.NumMethod() != 2 {
		t.Fatalf("Tool shape = %v with %d methods, want two-method interface", typeOf, typeOf.NumMethod())
	}
	want := map[string]reflect.Type{
		"Call":       reflect.TypeFor[func(context.Context, tool.Invocation) (chat.ToolOutput, error)](),
		"Definition": reflect.TypeFor[func() chat.ToolDefinition](),
	}
	for name, signature := range want {
		method, ok := typeOf.MethodByName(name)
		if !ok || method.Type != signature {
			t.Errorf("Tool.%s = %v (present %v), want %v", name, method.Type, ok, signature)
		}
	}
}

// Planning values must not prolong executable capability lifetimes merely to
// retain input admission rules. The independent input validator has its own
// lifetime regression test; every other function or interface remains forbidden.
// Follow Tool-owned fields to reject executors hidden behind private structs.
func TestContractAndInvocationDoNotRetainExecutionAuthority(t *testing.T) {
	owner := reflect.TypeFor[tool.Contract]().PkgPath()
	visited := make(map[reflect.Type]bool)
	var inspect func(reflect.Type)
	inspect = func(value reflect.Type) {
		if visited[value] {
			return
		}
		visited[value] = true
		switch value.Kind() {
		case reflect.Interface, reflect.Func:
			t.Errorf("planning representation retains executable authority through %v", value)
		case reflect.Pointer, reflect.Array, reflect.Slice, reflect.Chan:
			inspect(value.Elem())
		case reflect.Map:
			inspect(value.Key())
			inspect(value.Elem())
		case reflect.Struct:
			if value.PkgPath() != owner {
				return
			}
			for field := range value.Fields() {
				if value.Name() == "contractState" && field.Name == "validate" {
					if field.Type != reflect.TypeFor[func([]byte) error]() {
						t.Errorf("input validator type = %v", field.Type)
					}
					continue
				}
				inspect(field.Type)
			}
		}
	}
	inspect(reflect.TypeFor[tool.Contract]())
	inspect(reflect.TypeFor[tool.Invocation]())
}

func TestFuncStaysAnImmutableValueAdapter(t *testing.T) {
	typeOf := reflect.TypeFor[tool.Func[struct{}, string]]()
	methods := make([]string, 0, typeOf.NumMethod())
	for method := range typeOf.Methods() {
		methods = append(methods, method.Name)
	}
	slices.Sort(methods)
	if !slices.Equal(methods, []string{"Call", "Definition", "InputValidator"}) {
		t.Fatalf("Func methods = %v, want Call/Definition/InputValidator only", methods)
	}
	if !typeOf.Implements(reflect.TypeFor[tool.Tool]()) {
		t.Fatal("Func value does not implement Tool")
	}
	if !typeOf.Implements(reflect.TypeFor[tool.InputValidatingTool]()) {
		t.Fatal("Func value does not declare its input admission rules")
	}
	assertReceiverMethodsInFile(t, "Func", "function.go")
}

func assertReceiverMethodsInFile(t *testing.T, receiver, filename string) {
	t.Helper()
	fset := token.NewFileSet()
	root := moduleRoot(t)
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && (entry.Name() == "vendor" || entry.Name() == "internal" || strings.HasPrefix(entry.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv == nil || len(function.Recv.List) != 1 || receiverName(function.Recv.List[0].Type) != receiver {
				continue
			}
			if filepath.Base(path) != filename {
				t.Errorf("%s.%s is declared in %s, want %s", receiver, function.Name.Name, filepath.Base(path), filename)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect %s receiver methods: %v", receiver, err)
	}
}

func receiverName(expression ast.Expr) string {
	switch expression := expression.(type) {
	case *ast.Ident:
		return expression.Name
	case *ast.StarExpr:
		return receiverName(expression.X)
	case *ast.IndexExpr:
		return receiverName(expression.X)
	case *ast.IndexListExpr:
		return receiverName(expression.X)
	default:
		return ""
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not report test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
