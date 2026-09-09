package agent

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
)

func TestEngineEnforcesDispatcherEffectCapabilities(t *testing.T) {
	required, _ := ParseCapability("resource.read")
	definition := newCapabilityTestDefinition(t, required)
	dispatcher := &capabilityTestDispatcher{}
	deployment, err := NewDeployment(DeploymentConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: ComputeDigest([]byte("capability-test-implementation")),
		ConfigurationDigest:  ComputeDigest([]byte("capability-test-configuration")),
	})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(struct{}{})

	deniedEngine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	denied, err := deniedEngine.Run(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	if denied.Status() != StatusFailed || dispatcher.calls.Load() != 0 {
		t.Fatalf("denied status = %s, dispatcher calls = %d", denied.Status(), dispatcher.calls.Load())
	}
	if failure, ok := denied.Termination().Failure(); !ok || failure.Code() != "engine.capability.denied" {
		t.Fatalf("denied failure = %#v", failure)
	}
	if closeErr := deniedEngine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
		t.Fatal(closeErr)
	}

	capabilities, _ := NewCapabilitySet(required)
	allowedEngine, err := NewEngine(EngineConfig{Capabilities: capabilities})
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := allowedEngine.Run(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	if allowed.Status() != StatusCompleted || dispatcher.calls.Load() != 1 {
		t.Fatalf("allowed status = %s, dispatcher calls = %d", allowed.Status(), dispatcher.calls.Load())
	}
	if err := allowedEngine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

type capabilityTestDefinition struct {
	descriptor Descriptor
	required   Capability
}

func newCapabilityTestDefinition(t *testing.T, required Capability) *capabilityTestDefinition {
	t.Helper()
	inputSchema, _ := SchemaFor[struct{}]()
	outputSchema, _ := SchemaFor[struct{}]()
	descriptor, err := NewDescriptor(DescriptorConfig{
		Name: "test.capability", Description: "Verify Effect capability enforcement.",
		InputSchema: inputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &capabilityTestDefinition{descriptor: descriptor, required: required}
}

func (c *capabilityTestDefinition) Descriptor() Descriptor { return c.descriptor }

func (c *capabilityTestDefinition) Start(Input) (Execution, error) {
	return &capabilityTestExecution{required: c.required}, nil
}

func (c *capabilityTestDefinition) Restore(state ExecutionState) (Execution, error) {
	var phase uint8
	if err := json.Unmarshal(state.Payload(), &phase); err != nil {
		return nil, err
	}
	return &capabilityTestExecution{required: c.required, phase: phase}, nil
}

type capabilityTestExecution struct {
	required Capability
	phase    uint8
}

func (c *capabilityTestExecution) Step(context.Context, []Signal) (Transition, error) {
	if c.phase == 0 {
		effect, err := NewDispatcherEffect(json.RawMessage(`{}`), c.required)
		if err != nil {
			return Transition{}, err
		}
		c.phase = 1
		return Continue(0, effect)
	}
	c.phase = 2
	output, _ := EncodeOutput(struct{}{})
	return Complete(1, output)
}

func (c *capabilityTestExecution) Snapshot() (ExecutionState, error) {
	payload, _ := json.Marshal(c.phase)
	return NewExecutionState("test.capability", payload)
}

type capabilityTestDispatcher struct{ calls atomic.Int32 }

func (c *capabilityTestDispatcher) Dispatch(
	_ context.Context,
	request EffectRequest,
	_ DeltaEmitter,
) (Settlement, error) {
	c.calls.Add(1)
	return NewSettlement(request.ID(), SettlementStatusSucceeded, json.RawMessage(`{}`))
}

func (*capabilityTestDispatcher) ReplayPolicy(Effect) ReplayPolicy { return ReplayPolicyNever }
