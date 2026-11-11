package run

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/jsonschema"
)

type typedCount struct {
	Count int `json:"count"`
}

type typedFixtureDefinition struct {
	descriptor Descriptor
	starts     int
}

func (t *typedFixtureDefinition) Descriptor() Descriptor { return t.descriptor }

func (*typedFixtureDefinition) ChildPrograms() []Program[Payload, Payload] { return nil }

func (t *typedFixtureDefinition) Start(input Payload) (Execution, error) {
	t.starts++
	return &typedFixtureExecution{state: input}, nil
}

func (t *typedFixtureDefinition) Restore(ctx context.Context, state ExecutionState) (Execution, error) {
	return &typedFixtureExecution{state: state.Payload()}, nil
}

type typedFixtureExecution struct {
	state Payload
}

func (t *typedFixtureExecution) Step(context.Context, Turn) (Transition, error) {
	return Complete(0, t.state)
}

func (t *typedFixtureExecution) Snapshot() (ExecutionState, error) {
	return NewExecutionState("fixture", t.state)
}

func TestDescriptorOwnsTypedEdges(t *testing.T) {
	definition := newTypedFixtureDefinition[wireFixture](t, "fixture.message")
	descriptor := definition.Descriptor()
	input, err := descriptor.EncodeInput(wireFixture{Message: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	execution, err := definition.Start(input)
	if err != nil {
		t.Fatal(err)
	}
	transition, err := execution.Step(context.Background(), Turn{Process: controlValue(ParseProcessID("fixture")), Step: 1})
	if err != nil {
		t.Fatal(err)
	}
	output, ok := transition.Output()
	if !ok {
		t.Fatal("fixture did not complete")
	}
	decoded, err := descriptor.DecodeOutput[wireFixture](output)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Message != "hello" {
		t.Fatalf("decoded output = %+v", decoded)
	}
}

func TestDescriptorRejectsTypedSchemaMismatchAtEdge(t *testing.T) {
	definition := newTypedFixtureDefinition[wireFixture](t, "fixture.message")
	if _, err := definition.Descriptor().EncodeInput(typedCount{Count: 3}); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("EncodeInput schema mismatch error = %v, want ErrInvalidPayload", err)
	}
	if definition.starts != 0 {
		t.Fatalf("Definition.Start called %d times after edge validation failed", definition.starts)
	}
}

func TestInvalidDescriptorRejectsTypedEdges(t *testing.T) {
	if _, err := (Descriptor{}).EncodeInput(wireFixture{}); !errors.Is(err, ErrInvalidDescriptor) {
		t.Fatalf("EncodeInput error = %v, want ErrInvalidDescriptor", err)
	}
	output, err := EncodePayload(wireFixture{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (Descriptor{}).DecodeOutput[wireFixture](output); !errors.Is(err, ErrInvalidDescriptor) {
		t.Fatalf("DecodeOutput error = %v, want ErrInvalidDescriptor", err)
	}
}

func newTypedFixtureDefinition[T any](t *testing.T, name string) *typedFixtureDefinition {
	t.Helper()
	schema, err := jsonschema.For[T]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := NewDescriptor(DescriptorConfig{
		Name: name, Description: "A typed edge contract test fixture.",
		InputSchema: schema, OutputSchema: schema,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &typedFixtureDefinition{descriptor: descriptor}
}
