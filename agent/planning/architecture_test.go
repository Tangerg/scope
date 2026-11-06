package planning

import (
	"reflect"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
)

func TestStrategyDoesNotRetainProcessAuthority(t *testing.T) {
	for _, value := range []reflect.Type{
		reflect.TypeFor[definition](),
		reflect.TypeFor[execution](),
		reflect.TypeFor[dispatcher](),
	} {
		t.Run(value.Name(), func(t *testing.T) { conformancetest.AssertNoProcessAuthority(t, value) })
	}
}

func TestDefinitionDoesNotRetainDispatchCapabilities(t *testing.T) {
	capabilities := []reflect.Type{reflect.TypeFor[ActionExecutor](), reflect.TypeFor[Sensor]()}
	seen := make(map[reflect.Type]bool)
	var inspect func(reflect.Type, string)
	inspect = func(value reflect.Type, path string) {
		if seen[value] {
			return
		}
		seen[value] = true
		for _, capability := range capabilities {
			if value.Implements(capability) || value.Kind() != reflect.Interface && reflect.PointerTo(value).Implements(capability) {
				t.Errorf("pure planning state retains %v through %s", capability, path)
				return
			}
		}
		switch value.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan:
			inspect(value.Elem(), path+".element")
		case reflect.Map:
			inspect(value.Key(), path+".key")
			inspect(value.Elem(), path+".value")
		case reflect.Struct:
			for index := range value.NumField() {
				field := value.Field(index)
				inspect(field.Type, path+"."+field.Name)
			}
		case reflect.Func:
			for index := range value.NumIn() {
				inspect(value.In(index), path+".parameter")
			}
			for index := range value.NumOut() {
				inspect(value.Out(index), path+".result")
			}
		}
	}
	inspect(reflect.TypeFor[definition](), "definition")
	inspect(reflect.TypeFor[execution](), "execution")
}
