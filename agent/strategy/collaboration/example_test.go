package collaboration_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/collaboration"
	"github.com/Tangerg/scope/agent/strategy/coordination"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/agent/strategy/workflow"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
)

// The deterministic local model exercises the same interaction Dispatcher a
// provider model would use. The workflow owns the application's prompt and
// typed-output adaptation; collaboration only consumes Turn and Decision.
func ExampleDefinition() {
	ctx := context.Background()
	textSchema := exampleValue(agent.SchemaFor[string]())
	modelDefinition := exampleValue(interaction.NewDefinition(interaction.DefinitionConfig{
		Name: "example.decision_model", Description: "Choose the next collaboration action.", MaxModelCalls: agent.NewQuota(1),
	}))
	client := exampleValue(chatclient.New(decisionModel{}, chatclient.Config{}))
	dispatcher := exampleValue(interaction.NewDispatcher(modelDefinition, interaction.DispatcherConfig{Model: client}))
	model := exampleBinding(modelDefinition, dispatcher)
	budget := agent.Budget{Steps: agent.NewQuota(32), Effects: agent.NewQuota(16), Signals: agent.NewQuota(32)}
	render := exampleValue(workflow.Transform("render_turn", func(_ context.Context, turn collaboration.Turn) (interaction.Input, error) {
		payload, err := jsonv2.Marshal(turn)
		if err != nil {
			return interaction.Input{}, err
		}
		return interaction.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart(string(payload)))}}, nil
	}))
	call := exampleValue(workflow.Call(workflow.CallConfig{ID: "decide", Deployment: model, Budget: budget}))
	decode := exampleValue(workflow.Transform("decode_decision", func(_ context.Context, output interaction.Output) (collaboration.Decision, error) {
		if output.ModelResponse == nil {
			return collaboration.Decision{}, errors.New("coordinator produced no model response")
		}
		value, err := agent.ParsePayload([]byte(output.ModelResponse.Text()))
		if err != nil {
			return collaboration.Decision{}, err
		}
		return value.Decode[collaboration.Decision]()
	}))
	coordinator := exampleBinding(exampleValue(workflow.NewDefinition(workflow.DefinitionConfig{
		Name: "example.coordinator", Description: "Render a turn and decode a model decision.", Stages: []workflow.Stage{render, call, decode},
	})), nil)
	gate := exampleBinding(exampleValue(coordination.NewInputGate(coordination.InputGateConfig{
		Name: "example.input", Description: "Wait for an external instruction.", RequestSchema: textSchema, AnswerSchema: textSchema,
	})), nil)
	work := exampleValue(workflow.Transform("review", func(_ context.Context, task string) (string, error) {
		return "reviewed: " + task, nil
	}))
	worker := exampleBinding(exampleValue(workflow.NewDefinition(workflow.DefinitionConfig{
		Name: "example.reviewer", Description: "Review a task.", Stages: []workflow.Stage{work},
	})), nil)
	definition := exampleValue(collaboration.NewDefinition(collaboration.DefinitionConfig{
		Name: "example.collaboration", Description: "Run model decisions alongside background work.",
		Coordinator: collaboration.WorkerConfig{Deployment: coordinator, Budget: agent.Budget{Steps: agent.NewQuota(64), Effects: agent.NewQuota(32), Signals: agent.NewQuota(64)}},
		Workers:     []collaboration.WorkerConfig{{Deployment: gate, Budget: budget}, {Deployment: worker, Budget: budget}},
		StateSchema: textSchema, OutputSchema: textSchema, MaxTurns: agent.NewQuota(4), MaxTasks: agent.NewQuota(2), MaxConcurrentTasks: 2, MaxControlsPerTurn: 1,
	}))
	root := exampleBinding(definition, nil)
	engine := exampleValue(agent.NewEngine(agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter()}))
	defer func() {
		if err := engine.Close(ctx); err != nil {
			panic(err)
		}
	}()
	process := exampleValue(engine.Start(ctx, root, exampleValue(agent.EncodePayload("inspect deployment"))))
	result := exampleValue(process.Await(ctx))
	if err := process.Join(ctx); err != nil {
		panic(err)
	}
	if result.Termination().Status() != agent.StatusCompleted {
		panic(result.Termination().Reason())
	}
	output, _ := result.Termination().Output()
	fmt.Println(exampleValue(output.Decode[string]()))
	// Output:
	// coordinator continued while input was pending; reviewed: inspect deployment
}

type decisionModel struct{}

func (d decisionModel) Call(_ context.Context, request *chat.Request) (*chat.Response, error) {
	if len(request.Messages) != 1 {
		return nil, errors.New("expected one rendered turn")
	}
	input, err := agent.ParsePayload([]byte(request.Messages[0].Text()))
	if err != nil {
		return nil, err
	}
	turn, err := input.Decode[collaboration.Turn]()
	if err != nil {
		return nil, err
	}
	decision, err := d.decide(turn)
	if err != nil {
		return nil, err
	}
	payload, err := jsonv2.Marshal(decision)
	if err != nil {
		return nil, err
	}
	message := chat.NewAssistantMessage(chat.NewTextPart(string(payload)))
	return chat.NewResponse(&chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}, nil)
}

func (d decisionModel) decide(turn collaboration.Turn) (collaboration.Decision, error) {
	decision := collaboration.Decision{State: turn.State}
	switch turn.Number {
	case 1:
		decision.Mode = collaboration.ModeContinue
		decision.Tasks = []collaboration.TaskRequest{{Key: exampleValue(agent.ParseChildKey("input")), Worker: "example.input", Input: turn.State}}
	case 2:
		if len(turn.Tasks) != 1 || turn.Tasks[0].Start == nil || turn.Tasks[0].Outcome != nil {
			return decision, errors.New("background input task was not outstanding")
		}
		decision.Mode = collaboration.ModeWait
		reason := "No replacement instruction is needed."
		decision.Controls = []collaboration.Control{{Task: turn.Tasks[0].Request.Key, CancelReason: &reason}}
	case 3:
		if turn.Tasks[0].Outcome == nil || turn.Tasks[0].Outcome.Result().Termination().Status() != agent.StatusCanceled {
			return decision, errors.New("input cancellation did not drain")
		}
		decision.Mode = collaboration.ModeWait
		decision.Tasks = []collaboration.TaskRequest{{Key: exampleValue(agent.ParseChildKey("review")), Worker: "example.reviewer", Input: turn.State}}
	case 4:
		if len(turn.Tasks) != 2 || turn.Tasks[1].Outcome == nil {
			return decision, errors.New("review outcome is missing")
		}
		output, present := turn.Tasks[1].Outcome.Result().Termination().Output()
		if !present {
			return decision, errors.New("review produced no output")
		}
		review, err := output.Decode[string]()
		if err != nil {
			return decision, err
		}
		final, err := agent.EncodePayload("coordinator continued while input was pending; " + review)
		if err != nil {
			return decision, err
		}
		decision.Mode, decision.Output = collaboration.ModeUndecided, final
	default:
		return decision, errors.New("unexpected coordinator turn")
	}
	return decision, nil
}

func exampleValue[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func exampleBinding(definition agent.Definition, dispatcher agent.Dispatcher) agent.Deployment {
	// These labels identify the fixed code and configuration in this checked
	// example. Production bindings identify the built artifact and full config.
	return exampleValue(agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("collaboration-example-artifact")),
		ConfigurationDigest:  agent.ComputeDigest([]byte(definition.Descriptor().Name())),
	}))
}
