package agent_test

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"sync/atomic"

	"github.com/Tangerg/scope/agent"
)

type echoInput struct {
	Value string `json:"value"`
}

type echoOutput struct {
	Value string `json:"value"`
}

type echoDefinition struct {
	descriptor agent.Descriptor
}

type echoState struct {
	Phase string `json:"phase"`
	Value string `json:"value"`
}

func (e echoDefinition) Descriptor() agent.Descriptor { return e.descriptor }

func (e echoDefinition) Start(input agent.Input) (agent.Execution, error) {
	if err := e.descriptor.ValidateInput(input); err != nil {
		return nil, err
	}
	value, err := input.Decode[echoInput]()
	if err != nil {
		return nil, err
	}
	return &echoExecution{state: echoState{Phase: "ready", Value: value.Value}}, nil
}

func (echoDefinition) Restore(state agent.ExecutionState) (agent.Execution, error) {
	if !state.Valid() || state.Kind() != "example.echo" {
		return nil, agent.ErrInvalidExecutionState
	}
	var value echoState
	if err := jsonv2.Unmarshal(state.Payload(), &value, jsonv2.RejectUnknownMembers(true)); err != nil {
		return nil, err
	}
	switch value.Phase {
	case "ready", "awaiting_echo", "completed":
		return &echoExecution{state: value}, nil
	default:
		return nil, agent.ErrInvalidExecutionState
	}
}

type echoExecution struct {
	state echoState
}

func (e *echoExecution) Step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	if err := ctx.Err(); err != nil {
		return agent.Transition{}, err
	}
	switch e.state.Phase {
	case "ready":
		if len(signals) != 0 {
			return agent.Transition{}, agent.ErrInvalidSignal
		}
		payload, err := json.Marshal(echoInput{Value: e.state.Value})
		if err != nil {
			return agent.Transition{}, err
		}
		effect, err := agent.NewDispatcherEffect(payload)
		if err != nil {
			return agent.Transition{}, err
		}
		e.state.Phase = "awaiting_echo"
		return agent.Continue(0, effect)
	case "awaiting_echo":
		if len(signals) != 1 {
			return agent.Transition{}, agent.ErrInvalidSignal
		}
		var result echoOutput
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

func (e *echoExecution) Snapshot() (agent.ExecutionState, error) {
	payload, err := json.Marshal(e.state)
	if err != nil {
		return agent.ExecutionState{}, err
	}
	return agent.NewExecutionState("example.echo", payload)
}

func newEchoDefinition() (echoDefinition, error) {
	inputSchema, err := agent.SchemaFor[echoInput]()
	if err != nil {
		return echoDefinition{}, err
	}
	outputSchema, err := agent.SchemaFor[echoOutput]()
	if err != nil {
		return echoDefinition{}, err
	}
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name: "example.echo", Description: "Echoes a value through an Engine-managed Effect.",
		InputSchema: inputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		return echoDefinition{}, err
	}
	return echoDefinition{descriptor: descriptor}, nil
}

// echoDispatcher has no external mutation, so repeating an identity is safe.
type echoDispatcher struct{}

func (echoDispatcher) Dispatch(ctx context.Context, request agent.EffectRequest, emit agent.DeltaEmitter) (agent.Settlement, error) {
	if err := ctx.Err(); err != nil {
		return agent.Settlement{}, err
	}
	payload := request.Effect().Payload()
	if emit != nil {
		emit(payload)
	}
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
	ctx := context.Background()
	definition, err := newEchoDefinition()
	if err != nil {
		panic(err)
	}
	dispatcher := &countingDispatcher{next: echoDispatcher{}}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("example-echo-implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("example-echo-configuration")),
	})
	if err != nil {
		panic(err)
	}
	engine, err := agent.NewEngine(agent.EngineConfig{})
	if err != nil {
		panic(err)
	}
	defer func() {
		if closeErr := engine.Close(ctx); closeErr != nil {
			panic(closeErr)
		}
	}()
	input, err := definition.Descriptor().EncodeInput(echoInput{Value: "hello"})
	if err != nil {
		panic(err)
	}
	process, err := engine.Start(ctx, deployment, input)
	if err != nil {
		panic(err)
	}
	result, err := process.Await(ctx)
	if err != nil {
		panic(err)
	}
	if joinErr := process.Join(ctx); joinErr != nil {
		panic(joinErr)
	}
	output, ok := result.Output()
	if !ok {
		panic("completed Result has no Output")
	}
	value, err := definition.Descriptor().DecodeOutput[echoOutput](output)
	if err != nil {
		panic(err)
	}
	inspection, err := engine.InspectTree(ctx, result.ProcessID())
	if err != nil {
		panic(err)
	}
	report, found := inspection.Process(result.ProcessID())
	if !found {
		panic("completed Process is absent from its tree")
	}
	if err := engine.ReleaseTree(context.Background(), result.ProcessID()); err != nil {
		panic(err)
	}
	fmt.Println(result.Status(), value.Value, dispatcher.attempts.Load())
	fmt.Println("inspected:", report.Snapshot.Status(), report.Snapshot.Usage().PreparedEffects)
	// Output:
	// completed hello 1
	// inspected: completed 1
}
