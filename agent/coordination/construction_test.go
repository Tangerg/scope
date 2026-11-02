package coordination_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/coordination"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

func TestCoordinationRejectsIncompleteDefinitions(t *testing.T) {
	for _, definition := range []run.Definition{
		(*coordination.Deadline)(nil), &coordination.Deadline{},
		(*coordination.InputGate)(nil), &coordination.InputGate{},
		(*coordination.FirstSuccess)(nil), &coordination.FirstSuccess{},
	} {
		if definition.Descriptor().Valid() || len(definition.ChildPrograms()) != 0 {
			t.Fatalf("unconfigured %T exposed a usable contract", definition)
		}
		if execution, err := definition.Start(run.Payload{}); execution != nil || !errors.Is(err, coordination.ErrInvalidConfig) {
			t.Fatalf("unconfigured %T Start = %v, %v", definition, execution, err)
		}
		if execution, err := definition.Restore(t.Context(), run.ExecutionState{}); execution != nil || !errors.Is(err, coordination.ErrInvalidConfig) {
			t.Fatalf("unconfigured %T Restore = %v, %v", definition, execution, err)
		}
	}
}

func TestCoordinationRequiresNamedBoundedContracts(t *testing.T) {
	accept := func(context.Context, run.ChildKey, run.ChildOutcome) (bool, error) { return true, nil }
	textSchema, err := jsonschema.For[string]()
	if err != nil {
		t.Fatal(err)
	}
	for _, config := range []coordination.FirstSuccessConfig{
		{Name: "competition", Description: "Choose a result.", Accept: accept},
		{Name: "competition", Description: "Choose a result.", MaxCandidates: 1},
		{Name: "invalid name", Description: "Choose a result.", MaxCandidates: 1, Accept: accept},
	} {
		if definition, err := coordination.NewFirstSuccess(config); definition != nil || !errors.Is(err, coordination.ErrInvalidConfig) {
			t.Fatalf("invalid competition = %v, %v", definition, err)
		}
	}
	if definition, err := coordination.NewDeadline(coordination.DeadlineConfig{Name: "deadline"}); definition != nil || !errors.Is(err, coordination.ErrInvalidConfig) {
		t.Fatalf("unnamed deadline contract = %v, %v", definition, err)
	}
	for _, config := range []coordination.InputGateConfig{
		{Name: "gate", Description: "Wait for input.", RequestSchema: textSchema},
		{Name: "gate", Description: "Wait for input.", AnswerSchema: textSchema},
		{Name: "invalid name", Description: "Wait for input.", RequestSchema: textSchema, AnswerSchema: textSchema},
	} {
		if definition, err := coordination.NewInputGate(config); definition != nil || !errors.Is(err, coordination.ErrInvalidConfig) {
			t.Fatalf("invalid gate contract = %v, %v", definition, err)
		}
	}
}

func TestCoordinationRejectsInputOutsideItsContract(t *testing.T) {
	deadline := deadlineBinding(t, coordination.DeadlineDispatcher{}).Definition()
	first := competition(t, func(context.Context, run.ChildKey, run.ChildOutcome) (bool, error) { return true, nil }, 1)
	for _, sample := range []struct {
		definition run.Definition
		input      run.Payload
	}{
		{deadline, encodedInput(t, 1)},
		{deadline, encodedInput(t, "invalid time")},
		{deadline, encodedInput(t, time.Time{})},
		{inputGate(t), encodedInput(t, 1)},
		{first, encodedInput(t, "invalid candidates")},
		{first, encodedInput(t, []run.ChildSpec{})},
	} {
		if execution, err := sample.definition.Start(sample.input); execution != nil || !errors.Is(err, run.ErrInvalidPayload) {
			t.Fatalf("%T accepted %s: %v, %v", sample.definition, sample.input.JSON(), execution, err)
		}
	}
}
