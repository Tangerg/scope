package arch_test

import (
	"context"
	"reflect"
	"slices"
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
		"Call":       reflect.TypeFor[func(context.Context, tool.Invocation) (tool.Result, error)](),
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
}
