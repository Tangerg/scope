package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type typedCount struct {
	Count int `json:"count"`
}

type typedFixtureDefinition struct {
	descriptor Descriptor
	starts     int
}

func (t *typedFixtureDefinition) Descriptor() Descriptor { return t.descriptor }

func (t *typedFixtureDefinition) Start(input Input) (Execution, error) {
	t.starts++
	return &typedFixtureExecution{state: input.JSON()}, nil
}

func (t *typedFixtureDefinition) Restore(ctx context.Context, state ExecutionState) (Execution, error) {
	return &typedFixtureExecution{state: state.Payload()}, nil
}

type typedFixtureExecution struct {
	state json.RawMessage
}

func (t *typedFixtureExecution) Step(context.Context, []Signal) (Transition, error) {
	output, err := ParseOutput(t.state)
	if err != nil {
		return Transition{}, err
	}
	return Complete(0, output)
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
	transition, err := execution.Step(context.Background(), nil)
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
	if _, err := definition.Descriptor().EncodeInput(typedCount{Count: 3}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("EncodeInput schema mismatch error = %v, want ErrInvalidInput", err)
	}
	if definition.starts != 0 {
		t.Fatalf("Definition.Start called %d times after edge validation failed", definition.starts)
	}
}

func TestInvalidDescriptorRejectsTypedEdges(t *testing.T) {
	if _, err := (Descriptor{}).EncodeInput(wireFixture{}); !errors.Is(err, ErrInvalidDescriptor) {
		t.Fatalf("EncodeInput error = %v, want ErrInvalidDescriptor", err)
	}
	output, err := EncodeOutput(wireFixture{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (Descriptor{}).DecodeOutput[wireFixture](output); !errors.Is(err, ErrInvalidDescriptor) {
		t.Fatalf("DecodeOutput error = %v, want ErrInvalidDescriptor", err)
	}
}

func newTypedFixtureDefinition[T any](t *testing.T, name string) *typedFixtureDefinition {
	t.Helper()
	schema, err := SchemaFor[T]()
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
