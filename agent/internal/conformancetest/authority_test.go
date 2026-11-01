package conformancetest

import (
	"reflect"
	"testing"

	"github.com/Tangerg/scope/agent/run"
)

type processAccessor map[string]string

func (p processAccessor) Active() *run.Handle[run.Payload] { return nil }

func TestProcessAuthorityFollowsTypesAndPorts(t *testing.T) {
	for _, test := range []struct {
		value     reflect.Type
		forbidden bool
	}{
		{value: reflect.TypeFor[struct{ Value *run.Runtime }](), forbidden: true},
		{value: reflect.TypeFor[struct {
			Value map[string][]run.Store
		}](), forbidden: true},
		{value: reflect.TypeFor[struct {
			Value func() *run.Handle[run.Payload]
		}](), forbidden: true},
		{value: reflect.TypeFor[struct{ Value func(run.Store) }](), forbidden: true},
		{value: reflect.TypeFor[map[*run.Handle[run.Payload]]string](), forbidden: true},
		{value: reflect.TypeFor[interface {
			Resolve() run.ProgramResolver
		}](), forbidden: true},
		{value: reflect.TypeFor[struct{ Session string }]()},
		{value: reflect.TypeFor[processAccessor](), forbidden: true},
		{value: reflect.TypeFor[run.ProcessID]()},
		{value: reflect.TypeFor[run.EffectRequest]()},
	} {
		path := processAuthorityPath(test.value, make(map[reflect.Type]bool))
		if (path != "") != test.forbidden {
			t.Fatalf("%v: authority path = %q", test.value, path)
		}
	}
}
