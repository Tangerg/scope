package workflow

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/run/runtest"
)

func TestAdaptMapsAChildToAnotherRole(t *testing.T) {
	compiler := conformancetest.Compiler(t)
	child, err := Compile[numberInput, numberOutput](compiler, Config{
		Name: "test.adapt_child", Description: "Double one number.", Revision: "double-v1",
		Stages: []Stage{mustTransform(t, "double", func(_ context.Context, input numberInput) (numberOutput, error) {
			return numberOutput{Value: input.Value * 2}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	adapted, err := Adapt(compiler, AdaptConfig{
		Name: "test.adapt", Description: "Render a doubled number.", Revision: "render-v1", Budget: mustBudget(t),
	}, child, func(_ context.Context, input textValue) (numberInput, error) {
		value, parseErr := strconv.Atoi(input.Text)
		return numberInput{Value: value}, parseErr
	}, func(_ context.Context, output numberOutput) (textValue, error) {
		return textValue{Text: strconv.Itoa(output.Value)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	runtime, err := run.New(t.Context(), run.Config{Build: conformancetest.Build, Store: run.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := runtime.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	succeeded := observeAdapted(t, runtime, adapted, textValue{Text: "21"})
	if output, present := succeeded.Output(); !present || mustDecodeText(t, output) != "42" {
		t.Fatalf("adapted termination = %#v", succeeded)
	}
	failed := observeAdapted(t, runtime, adapted, textValue{Text: "twenty-one"})
	if failed.Status() != run.StatusFailed {
		t.Fatalf("unmappable input status = %s", failed.Status())
	}
}

func TestAdaptRejectsAMissingMapping(t *testing.T) {
	compiler := conformancetest.Compiler(t)
	child := mustProgram(t, mustDefinition(t, "test.identity", mustTransform(t, "identity", func(_ context.Context, value int) (int, error) { return value, nil })), "identity")
	config := AdaptConfig{Name: "test.adapt", Description: "Adapt one child.", Revision: "adapt-v1", Budget: mustBudget(t)}
	if _, err := Adapt[int, run.Payload, run.Payload, int](compiler, config, child, nil, nil); !errors.Is(err, ErrInvalidStage) {
		t.Fatalf("missing mappings error = %v", err)
	}
}

func observeAdapted(t *testing.T, runtime *run.Runtime, program run.Program[textValue, textValue], input textValue) run.Termination {
	t.Helper()
	erased, err := run.EncodePayload(input)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtest.Observe(t.Context(), runtime, program.Erase(), erased)
	if err != nil {
		t.Fatal(err)
	}
	return result.Termination()
}

func mustDecodeText(t *testing.T, output run.Payload) string {
	t.Helper()
	value, err := output.Decode[textValue]()
	if err != nil {
		t.Fatal(err)
	}
	return value.Text
}
