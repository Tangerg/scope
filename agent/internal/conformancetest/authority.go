package conformancetest

import (
	"reflect"
	"testing"

	agent "github.com/Tangerg/scope/agent"
)

// AssertNoProcessAuthority checks that a Strategy's retained types and ports
// cannot own or control managed Processes. Names and source files do not confer
// authority; Engine, Process, durability, and resolution capabilities do.
func AssertNoProcessAuthority(t *testing.T, value reflect.Type) {
	t.Helper()
	if path := processAuthorityPath(value, make(map[reflect.Type]bool)); path != "" {
		t.Errorf("%v carries managed Process authority through %s", value, path)
	}
}

func processAuthorityPath(value reflect.Type, seen map[reflect.Type]bool) string {
	if seen[value] {
		return ""
	}
	seen[value] = true
	switch value {
	case reflect.TypeFor[agent.Engine](), reflect.TypeFor[agent.Process](),
		reflect.TypeFor[agent.TreeDurability](), reflect.TypeFor[agent.DeploymentResolver]():
		return value.String()
	}
	visit := func(name string, child reflect.Type) string {
		if path := processAuthorityPath(child, seen); path != "" {
			return name + "." + path
		}
		return ""
	}
	switch value.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan:
		if path := visit("element", value.Elem()); path != "" {
			return path
		}
	case reflect.Map:
		if path := visit("key", value.Key()); path != "" {
			return path
		}
		if path := visit("value", value.Elem()); path != "" {
			return path
		}
	case reflect.Struct:
		for index := range value.NumField() {
			field := value.Field(index)
			if path := visit(field.Name, field.Type); path != "" {
				return path
			}
		}
	case reflect.Func:
		for index := range value.NumIn() {
			if path := visit("parameter", value.In(index)); path != "" {
				return path
			}
		}
		for index := range value.NumOut() {
			if path := visit("result", value.Out(index)); path != "" {
				return path
			}
		}
	}
	methods := value
	if value.Kind() != reflect.Interface && value.Kind() != reflect.Pointer {
		methods = reflect.PointerTo(value)
	}
	for index := range methods.NumMethod() {
		method := methods.Method(index)
		if path := visit(method.Name, method.Type); path != "" {
			return path
		}
	}
	return ""
}
