package team_test

import (
	startrand "crypto/rand"

	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/coordination"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/team"
	"github.com/Tangerg/scope/agent/workflow"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/core/jsonschema"
)

// The deterministic local model exercises the same interaction Dispatcher a
// provider model would use. The role owns the application's prompt and
// typed-output adaptation; collaboration only consumes Turn and Decision.
func ExampleDefinition() {
	ctx := context.Background()
	textSchema := exampleValue(jsonschema.For[string]())
	client := exampleValue(chatclient.New(decisionModel{}, chatclient.Config{}))
	model := exampleValue(agent.Compile(ctx, agent.Config{Name: "example.decision_model", Description: "Choose the next team decision.", MaxModelCalls: run.NewQuota(1), Model: client, BuildIdentity: run.ComputeDigest([]byte("example decision build")), ModelIdentity: run.ComputeDigest([]byte("example decision settings"))}))
	budget := run.Budget{Steps: run.NewQuota(32), Effects: run.NewQuota(16), Signals: run.NewQuota(32)}
	coordinator := exampleCoordinator(model, budget)
	gate := exampleBinding(exampleValue(coordination.NewInputGate(coordination.InputGateConfig{
		Name: "example.input", Description: "Wait for an external instruction.", RequestSchema: textSchema, AnswerSchema: textSchema,
	})), nil)
	work := exampleValue(workflow.Transform("review", func(_ context.Context, task string) (string, error) {
		return "reviewed: " + task, nil
	}))
	worker := exampleBinding(exampleValue(workflow.NewDefinition(workflow.DefinitionConfig{
		Name: "example.reviewer", Description: "Review a task.", Stages: []workflow.Stage{work},
	})), nil)
	root := exampleValue(team.Compile[string, string](ctx, team.Config{BuildIdentity: run.ComputeDigest([]byte("example team")),
		Name: "example.collaboration", Description: "Run model decisions alongside background work.",
		Coordinator: team.WorkerConfig{Program: coordinator, Budget: run.Budget{Steps: run.NewQuota(64), Effects: run.NewQuota(32), Signals: run.NewQuota(64)}},
		Workers:     []team.WorkerConfig{{Program: gate, Budget: budget}, {Program: worker, Budget: budget}},
		MaxTurns:    run.NewQuota(4), MaxTasks: run.NewQuota(2), MaxConcurrentTasks: 2, MaxControlsPerTurn: 1,
	}))

	engine := exampleValue(run.New(context.Background(), run.Config{Store: run.NewMemoryStore()}))
	defer func() {
		if err := engine.Close(ctx); err != nil {
			panic(err)
		}
	}()
	process := exampleValue(engine.Start(ctx, root, run.StartRequest[string]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: "inspect deployment"}))
	result := exampleValue(process.Await(ctx))
	if err := process.Join(ctx); err != nil {
		panic(err)
	}
	if result.Termination().Status() != run.StatusCompleted {
		panic(result.Termination().Reason())
	}
	output, _ := result.Termination().Output()
	fmt.Println(exampleValue(output.Decode[string]()))
	// Output:
	// coordinator continued while input was pending; reviewed: inspect deployment
}

func exampleCoordinator(model run.Program[agent.Input, agent.Output], budget run.Budget) run.Program[run.Payload, run.Payload] {
	return exampleValue(agent.Adapt(context.Background(), agent.AdapterConfig[team.Turn, team.Decision]{
		Name: "example.coordinator", Description: "Render a turn and decode a completed model decision.",
		Interaction: model, Budget: budget,
		BuildInput: func(_ context.Context, turn team.Turn) (agent.Input, error) {
			payload, err := jsonv2.Marshal(turn)
			if err != nil {
				return agent.Input{}, err
			}
			return agent.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart(string(payload)))}}, nil
		},
		DecodeOutput: func(_ context.Context, output agent.Output) (team.Decision, error) {
			return chatclient.JSON[team.Decision]().Decode(output.ModelResponse)
		},
		ImplementationDigest: run.ComputeDigest([]byte("collaboration-coordinator-role")),
		ConfigurationDigest:  run.ComputeDigest(exampleValue(jsonv2.Marshal(budget))),
	})).Erase()
}

type decisionModel struct{}

func (d decisionModel) Call(_ context.Context, request *chat.Request) (*chat.Response, error) {
	if len(request.Messages) != 1 {
		return nil, errors.New("expected one rendered turn")
	}
	input, err := run.ParsePayload([]byte(request.Messages[0].Text()))
	if err != nil {
		return nil, err
	}
	turn, err := input.Decode[team.Turn]()
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

func (d decisionModel) decide(turn team.Turn) (team.Decision, error) {
	decision := team.Decision{State: turn.State}
	switch turn.Number {
	case 1:
		decision.Mode = team.ModeContinue
		decision.Tasks = []team.TaskRequest{{Key: exampleValue(run.ParseChildKey("input")), Worker: "example.input", Input: turn.State}}
	case 2:
		if len(turn.Tasks) != 1 || turn.Tasks[0].Start == nil || turn.Tasks[0].Outcome != nil {
			return decision, errors.New("background input task was not outstanding")
		}
		decision.Mode = team.ModeWait
		reason := "No replacement instruction is needed."
		decision.Controls = []team.Control{{Task: turn.Tasks[0].Request.Key, CancelReason: &reason}}
	case 3:
		if turn.Tasks[0].Outcome == nil || turn.Tasks[0].Outcome.Result().Termination().Status() != run.StatusCanceled {
			return decision, errors.New("input cancellation did not drain")
		}
		decision.Mode = team.ModeWait
		decision.Tasks = []team.TaskRequest{{Key: exampleValue(run.ParseChildKey("review")), Worker: "example.reviewer", Input: turn.State}}
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
		final, err := run.EncodePayload("coordinator continued while input was pending; " + review)
		if err != nil {
			return decision, err
		}
		decision.Mode, decision.Output = team.ModeUndecided, final
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

func exampleBinding(definition run.Definition, dispatcher run.Dispatcher) run.Program[run.Payload, run.Payload] {
	// These labels identify the fixed code and configuration in this checked
	// example. Production bindings identify the built artifact and full config.
	return exampleValue(run.Bind[run.Payload, run.Payload](run.BindingConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: run.ComputeDigest([]byte("collaboration-example-artifact")),
		ConfigurationDigest:  run.ComputeDigest([]byte(definition.Descriptor().Name())),
	}))
}
