package run

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/jsonschema"
)

func TestEmptyCapabilitySetHasOneArrayRepresentation(t *testing.T) {
	encoded, err := jsonv2.Marshal(CapabilitySet{})
	if err != nil || string(encoded) != "[]" {
		t.Fatalf("empty capability wire = %s, error=%v", encoded, err)
	}
	var capabilities CapabilitySet
	if err := jsonv2.Unmarshal([]byte("null"), &capabilities); !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("null capability set = %v", err)
	}
	if err := jsonv2.Unmarshal(encoded, &capabilities); err != nil || len(capabilities.Values()) != 0 {
		t.Fatalf("empty capability round trip = %+v, error=%v", capabilities, err)
	}
}

func TestRuntimeEnforcesDispatcherEffectCapabilities(t *testing.T) {
	required, _ := ParseCapability("resource.read")
	definition := newCapabilityTestDefinition(t)
	dispatcher := &capabilityTestDispatcher{required: required}
	compilerRuntime := controlValue(New(t.Context(), Config{Store: NewMemoryStore(), Build: "test"}))
	program, err := Bind[Payload, Payload](compilerRuntime.Compiler(), BindingConfig{
		Definition: definition, Dispatcher: dispatcher,
		Revision: "capability-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodePayload(struct{}{})

	deniedRuntime, err := New(t.Context(), Config{Build: "test", Store: NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	denied, err := observeOutcome(context.Background(), deniedRuntime, program, input)
	if err != nil {
		t.Fatal(err)
	}
	if denied.Termination().Status() != StatusFailed || dispatcher.calls.Load() != 0 {
		t.Fatalf("denied status = %s, dispatcher calls = %d", denied.Termination().Status(), dispatcher.calls.Load())
	}
	if failure, ok := denied.Termination().Failure(); !ok || failure.Code() != "runtime.capability.denied" {
		t.Fatalf("denied failure = %#v", failure)
	}
	if closeErr := deniedRuntime.Close(context.WithoutCancel(t.Context())); closeErr != nil {
		t.Fatal(closeErr)
	}

	capabilities, _ := NewCapabilitySet(required)
	allowedRuntime, err := New(t.Context(), Config{Build: "test", Store: NewMemoryStore(), Capabilities: capabilities})
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := observeOutcome(context.Background(), allowedRuntime, program, input)
	if err != nil {
		t.Fatal(err)
	}
	if allowed.Termination().Status() != StatusCompleted || dispatcher.calls.Load() != 1 {
		t.Fatalf("allowed status = %s, dispatcher calls = %d", allowed.Termination().Status(), dispatcher.calls.Load())
	}
	if err := allowedRuntime.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

type capabilityTestDefinition struct {
	descriptor Descriptor
}

func newCapabilityTestDefinition(t *testing.T) *capabilityTestDefinition {
	t.Helper()
	inputSchema, _ := jsonschema.For[struct{}]()
	outputSchema, _ := jsonschema.For[struct{}]()
	descriptor, err := NewDescriptor(DescriptorConfig{
		Name: "test.capability", Description: "Verify Effect capability enforcement.",
		InputSchema: inputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &capabilityTestDefinition{descriptor: descriptor}
}

func (c *capabilityTestDefinition) Descriptor() Descriptor { return c.descriptor }

func (*capabilityTestDefinition) ChildPrograms() []Program[Payload, Payload] { return nil }

func (c *capabilityTestDefinition) Start(Payload) (Execution, error) {
	return &capabilityTestExecution{}, nil
}

func (c *capabilityTestDefinition) Restore(ctx context.Context, state ExecutionState) (Execution, error) {
	var phase uint8
	if err := jsonv2.Unmarshal(state.Payload(), &phase); err != nil {
		return nil, err
	}
	return &capabilityTestExecution{phase: phase}, nil
}

type capabilityTestExecution struct {
	phase uint8
}

func (c *capabilityTestExecution) Step(context.Context, Turn) (Transition, error) {
	if c.phase == 0 {
		effect, err := NewDispatcherEffect(json.RawMessage(`{}`))
		if err != nil {
			return Transition{}, err
		}
		c.phase = 1
		return Yield(0, []Effect{effect})
	}
	c.phase = 2
	output, _ := EncodePayload(struct{}{})
	return Complete(1, output)
}

func (c *capabilityTestExecution) Snapshot() (ExecutionState, error) {
	payload, _ := jsonv2.Marshal(c.phase)
	return ParseExecutionState("test.capability", payload)
}

// capabilityTestDispatcher declares one required capability for every Effect.
type capabilityTestDispatcher struct {
	required Capability
	calls    atomic.Int32
}

func (c *capabilityTestDispatcher) Dispatch(
	_ context.Context,
	request EffectRequest,
	_ DeltaEmitter,
) (Settlement, error) {
	c.calls.Add(1)
	return NewSettlement(SettlementStatusSucceeded, json.RawMessage(`{}`))
}

func (c *capabilityTestDispatcher) Policy(Effect) EffectPolicy {
	return EffectPolicy{Replay: ReplayPolicyNever, RequiredCapabilities: controlValue(NewCapabilitySet(c.required))}
}
