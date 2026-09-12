package workflow

import (
	"reflect"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
)

func TestStrategyDoesNotRetainProcessAuthority(t *testing.T) {
	for _, value := range []reflect.Type{
		reflect.TypeFor[Definition](),
		reflect.TypeFor[execution](),
	} {
		t.Run(value.Name(), func(t *testing.T) { conformancetest.AssertNoProcessAuthority(t, value) })
	}
}

func TestWorkflowStageAlgebraRemainsSealed(t *testing.T) {
	stage := reflect.TypeFor[Stage]()
	if stage.Kind() != reflect.Struct {
		t.Fatal("Stage must be a constructed value")
	}
	for index := range stage.NumField() {
		if field := stage.Field(index); field.IsExported() {
			t.Errorf("Stage exposes mutable behavior through %s", field.Name)
		}
	}
}
