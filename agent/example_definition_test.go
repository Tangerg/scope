package agent_test

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"sync/atomic"

	"github.com/Tangerg/scope/agent"
)

type externalInput struct {
	Value string `json:"value"`
}

type externalOutput struct {
	Value string `json:"value"`
}

type externalDefinition struct {
	descriptor agent.Descriptor
}

type externalState struct {
	Phase string `json:"phase"`
	Value string `json:"value"`
}

func (e externalDefinition) Descriptor() agent.Descriptor { return e.descriptor }

func (e externalDefinition) Start(input agent.Input) (agent.Execution, error) {
	if err := e.descriptor.ValidateInput(input); err != nil {
		return nil, err
	}
	value, err := input.Decode[externalInput]()
	if err != nil {
		return nil, err
	}
	return &externalExecution{state: externalState{Phase: "ready", Value: value.Value}}, nil
}

func (externalDefinition) Restore(state agent.ExecutionState) (agent.Execution, error) {
	if !state.Valid() || state.Kind() != "external.direct" {
		return nil, agent.ErrInvalidExecutionState
	}
	var value externalState
	if err := jsonv2.Unmarshal(state.Payload(), &value, jsonv2.RejectUnknownMembers(true)); err != nil {
		return nil, err
	}
	switch value.Phase {
	case "ready", "dispatched", "completed":
		return &externalExecution{state: value}, nil
	default:
		return nil, agent.ErrInvalidExecutionState
	}
}

type externalExecution struct {
	state externalState
}

func (e *externalExecution) Step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	if err := ctx.Err(); err != nil {
		return agent.Transition{}, err
	}
	switch e.state.Phase {
	case "ready":
		if len(signals) != 0 {
			return agent.Transition{}, agent.ErrInvalidSignal
		}
		payload, err := json.Marshal(externalInput{Value: e.state.Value})
		if err != nil {
			return agent.Transition{}, err
		}
		effect, err := agent.NewDispatcherEffect(payload)
		if err != nil {
			return agent.Transition{}, err
		}
		e.state.Phase = "dispatched"
		return agent.Continue(0, effect)
	case "dispatched":
		if len(signals) != 1 {
			return agent.Transition{}, agent.ErrInvalidSignal
		}
		var result externalOutput
		if err := jsonv2.Unmarshal(signals[0].Payload(), &result, jsonv2.RejectUnknownMembers(true)); err != nil {
			return agent.Transition{}, err
		}
		output, err := agent.EncodeOutput(result)
		if err != nil {
			return agent.Transition{}, err
		}
		e.state.Phase = "completed"
		return agent.Complete(1, output)
	default:
		return agent.Transition{}, agent.ErrInvalidExecutionState
	}
}

func (e *externalExecution) Snapshot() (agent.ExecutionState, error) {
	payload, err := json.Marshal(e.state)
	if err != nil {
		return agent.ExecutionState{}, err
	}
	return agent.NewExecutionState("external.direct", payload)
}

func newExternalDefinition() (externalDefinition, error) {
	inputSchema, err := agent.SchemaFor[externalInput]()
	if err != nil {
		return externalDefinition{}, err
	}
	outputSchema, err := agent.SchemaFor[externalOutput]()
	if err != nil {
		return externalDefinition{}, err
	}
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name: "external.direct", Description: "Completes a direct external API example.",
		InputSchema: inputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		return externalDefinition{}, err
	}
	return externalDefinition{descriptor: descriptor}, nil
}

// echoDispatcher has no external mutation, so repeating an identity is safe.
type echoDispatcher struct{}

func (echoDispatcher) Dispatch(ctx context.Context, request agent.EffectRequest, emit agent.DeltaEmitter) (agent.Settlement, error) {
	if err := ctx.Err(); err != nil {
		return agent.Settlement{}, err
	}
	payload := request.Effect().Payload()
	emit(payload)
	return agent.NewSettlement(request.ID(), agent.SettlementStatusSucceeded, payload)
}

func (echoDispatcher) ReplayPolicy(agent.Effect) agent.ReplayPolicy {
	return agent.ReplayPolicySameIdentity
}

// countingDispatcher counts attempts, including replay, rather than logical operations.
type countingDispatcher struct {
	next     agent.Dispatcher
	attempts atomic.Int64
}

func (c *countingDispatcher) Dispatch(ctx context.Context, request agent.EffectRequest, emit agent.DeltaEmitter) (agent.Settlement, error) {
	c.attempts.Add(1)
	return c.next.Dispatch(ctx, request, emit)
}

func (c *countingDispatcher) ReplayPolicy(effect agent.Effect) agent.ReplayPolicy {
	return c.next.ReplayPolicy(effect)
}

// An independently authored Definition binds through the same Deployment and
// Engine contracts as built-in strategies. TestExternalPackageCanComposeAndRunDefinition
// checks this implementation with agenttest.RunDefinitionConformance.
func ExampleDefinition() {
	definition, err := newExternalDefinition()
	if err != nil {
		panic(err)
	}
	dispatcher := &countingDispatcher{next: echoDispatcher{}}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("external-direct-implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("external-direct-configuration")),
	})
	if err != nil {
		panic(err)
	}
	engine, err := agent.NewEngine(agent.EngineConfig{})
	if err != nil {
		panic(err)
	}
	defer func() {
		if closeErr := engine.Close(); closeErr != nil {
			panic(closeErr)
		}
	}()
	input, err := definition.Descriptor().EncodeInput(externalInput{Value: "hello"})
	if err != nil {
		panic(err)
	}
	result, err := engine.Run(context.Background(), deployment, input)
	if err != nil {
		panic(err)
	}
	output, ok := result.Output()
	if !ok {
		panic("completed Result has no Output")
	}
	value, err := definition.Descriptor().DecodeOutput[externalOutput](output)
	if err != nil {
		panic(err)
	}
	if err := engine.ReleaseTree(context.Background(), result.ProcessID()); err != nil {
		panic(err)
	}
	fmt.Println(result.Status(), value.Value, dispatcher.attempts.Load())
	// Output:
	// completed hello 1
}
