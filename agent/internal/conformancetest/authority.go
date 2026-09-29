package conformancetest

import (
	"reflect"
	"testing"

	agent "github.com/Tangerg/scope/agent"
)

// AssertNoProcessAuthority checks that a Strategy's retained types and ports
// cannot own or control managed Processes. Names and source files do not confer
// authority; Engine, Process, committer, and resolution capabilities do.
func AssertNoProcessAuthority(t *testing.T, value reflect.Type) {
	t.Helper()
	if path := processAuthorityPath(value, make(map[reflect.Type]bool)); path != "" {
		t.Errorf("%v carries managed Process authority through %s", value, path)
	}
}

var processAuthorityTypes = map[reflect.Type]bool{
	reflect.TypeFor[agent.Engine]():             true,
	reflect.TypeFor[agent.Process]():            true,
	reflect.TypeFor[agent.TreeCommitter]():      true,
	reflect.TypeFor[agent.DeploymentResolver](): true,
}

func processAuthorityPath(value reflect.Type, seen map[reflect.Type]bool) string {
	if seen[value] {
		return ""
	}
	seen[value] = true
	if processAuthorityTypes[value] {
		return value.String()
	}
	for _, edge := range reachableTypes(value) {
		if path := processAuthorityPath(edge.value, seen); path != "" {
			return edge.name + "." + path
		}
	}
	return ""
}

type typeEdge struct {
	name  string
	value reflect.Type
}

// reachableTypes includes the methods of the pointer type because a value
// stored by a Strategy can still expose pointer-receiver capabilities.
func reachableTypes(value reflect.Type) []typeEdge {
	var edges []typeEdge
	switch value.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan:
		edges = append(edges, typeEdge{"element", value.Elem()})
	case reflect.Map:
		edges = append(edges, typeEdge{"key", value.Key()}, typeEdge{"value", value.Elem()})
	case reflect.Struct:
		for index := range value.NumField() {
			field := value.Field(index)
			edges = append(edges, typeEdge{field.Name, field.Type})
		}
	case reflect.Func:
		for index := range value.NumIn() {
			edges = append(edges, typeEdge{"parameter", value.In(index)})
		}
		for index := range value.NumOut() {
			edges = append(edges, typeEdge{"result", value.Out(index)})
		}
	}
	methods := value
	if value.Kind() != reflect.Interface && value.Kind() != reflect.Pointer {
		methods = reflect.PointerTo(value)
	}
	for index := range methods.NumMethod() {
		method := methods.Method(index)
		edges = append(edges, typeEdge{method.Name, method.Type})
	}
	return edges
}
