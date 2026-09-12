package planning

import (
	"reflect"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
)

func TestStrategyDoesNotRetainProcessAuthority(t *testing.T) {
	for _, value := range []reflect.Type{
		reflect.TypeFor[Definition](),
		reflect.TypeFor[execution](),
		reflect.TypeFor[Dispatcher](),
	} {
		t.Run(value.Name(), func(t *testing.T) { conformancetest.AssertNoProcessAuthority(t, value) })
	}
}
