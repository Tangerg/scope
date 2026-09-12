package conformancetest

import (
	"reflect"
	"testing"

	agent "github.com/Tangerg/scope/agent"
)

type processAccessor map[string]string

func (p processAccessor) Active() *agent.Process { return nil }

func TestProcessAuthorityFollowsTypesAndPorts(t *testing.T) {
	for _, test := range []struct {
		value     reflect.Type
		forbidden bool
	}{
		{value: reflect.TypeFor[struct{ Value *agent.Engine }](), forbidden: true},
		{value: reflect.TypeFor[struct {
			Value map[string][]agent.TreeDurability
		}](), forbidden: true},
		{value: reflect.TypeFor[struct{ Value func() *agent.Process }](), forbidden: true},
		{value: reflect.TypeFor[interface {
			Resolve() agent.DeploymentResolver
		}](), forbidden: true},
		{value: reflect.TypeFor[struct{ Session string }]()},
		{value: reflect.TypeFor[processAccessor](), forbidden: true},
		{value: reflect.TypeFor[agent.ProcessID]()},
		{value: reflect.TypeFor[agent.EffectRequest]()},
	} {
		path := processAuthorityPath(test.value, make(map[reflect.Type]bool))
		if (path != "") != test.forbidden {
			t.Fatalf("%v: authority path = %q", test.value, path)
		}
	}
}
