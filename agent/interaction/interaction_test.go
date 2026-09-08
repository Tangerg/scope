package interaction_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/core/tool"
)

func TestManagedInteractionCompletesFromModelResponse(t *testing.T) {
	model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		if len(request.Tools) != 0 {
			t.Fatalf("model request has %d tools, want none", len(request.Tools))
		}
		return textResponse("done"), nil
	})
	deployment := newDeployment(t, model, nil, 2)
	engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: deployment.resolver})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(); closeErr != nil {
			t.Errorf("Close: %v", closeErr)
		}
	})

	input, err := agent.EncodeInput(interaction.Input{
		Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("finish"))},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(context.Background(), deployment.Deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status() != agent.StatusCompleted {
		t.Fatalf("status = %s, want completed; termination = %#v", result.Status(), result.Termination())
	}
	erased, ok := result.Output()
	if !ok {
		t.Fatal("completed Interaction has no output")
	}
	output, err := erased.Decode[interaction.Output]()
	if err != nil {
		t.Fatal(err)
	}
	if output.Source != interaction.CompletionSourceModelResponse || output.ModelResponse == nil ||
		output.ModelCalls != 1 || output.ModelResponse.Text() != "done" {
		t.Fatalf("output = %#v", output)
	}
}

func TestManagedInteractionExecutesToolLoopInModelOrder(t *testing.T) {
	type addInput struct {
		Left  int `json:"left"`
		Right int `json:"right"`
	}
	add, err := tool.NewFunc(tool.FuncConfig{
		Name:        "add",
		Description: "Add two integers and return their sum.",
	}, func(_ context.Context, input addInput) (int, error) {
		return input.Left + input.Right, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	model := &scriptedModel{}
	deployment := newDeployment(t, model, []tool.Tool{add}, 3)
	engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: deployment.resolver})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(); closeErr != nil {
			t.Errorf("Close: %v", closeErr)
		}
	})
	input, err := agent.EncodeInput(interaction.Input{
		Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("add 2 and 3"))},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(context.Background(), deployment.Deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status() != agent.StatusCompleted {
		t.Fatalf("status = %s, termination = %#v", result.Status(), result.Termination())
	}
	erased, _ := result.Output()
	output, err := erased.Decode[interaction.Output]()
	if err != nil {
		t.Fatal(err)
	}
	if output.Source != interaction.CompletionSourceModelResponse || output.ModelResponse == nil ||
		output.ModelCalls != 2 || output.ModelResponse.Text() != "5" {
		t.Fatalf("output = %#v", output)
	}
	if model.Calls() != 2 {
		t.Fatalf("model calls = %d, want 2", model.Calls())
	}
}

func TestManagedInteractionTerminatesOnModelHostFailure(t *testing.T) {
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return nil, interaction.HostFailure(errors.New("model boundary unavailable"))
	})
	result := runInteraction(t, newDeployment(t, model, nil, 2), "fail before provider")
	assertInteractionHostFailure(t, result)
}

func TestManagedInteractionPreservesUnknownToolOutcomes(t *testing.T) {
	inputRequired := interaction.RequireToolInput(
		json.RawMessage(`"continue?"`), json.RawMessage(`{"type":"boolean"}`), json.RawMessage(`{}`),
	)
	if !errors.Is(inputRequired, interaction.ErrToolInputRequired) {
		t.Fatal(inputRequired)
	}
	for _, testCase := range []struct {
		name   string
		cause  error
		panics bool
	}{
		{name: "host failure", cause: interaction.HostFailure(errors.New("tool boundary unavailable"))},
		{name: "cancellation", cause: context.Canceled},
		{name: "deadline", cause: context.DeadlineExceeded},
		{name: "host failure with input request", cause: interaction.HostFailure(inputRequired)},
		{name: "cancellation with input request", cause: errors.Join(inputRequired, context.Canceled)},
		{name: "deadline with input request", cause: errors.Join(context.DeadlineExceeded, inputRequired)},
		{name: "panic", panics: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			type input struct{}
			failing, err := tool.NewFunc(tool.FuncConfig{
				Name: "failing", Description: "Lose the result at the Tool boundary.",
			}, func(context.Context, input) (string, error) {
				if testCase.panics {
					panic("tool result unavailable")
				}
				return "", testCase.cause
			})
			if err != nil {
				t.Fatal(err)
			}
			model := &singleToolCallModel{call: chat.ToolCall{ID: "call_unknown", Name: "failing", Arguments: `{}`}}
			observer := &toolSettlementObserver{settlements: make(chan interaction.ToolSettlement, 1)}
			deployment := configuredInteraction(t, interaction.DefinitionConfig{
				Name: "interaction.unknown_tool", Description: "Preserve unknown Tool outcomes.", MaxModelCalls: 2,
			}, interaction.DispatcherConfig{Client: model}, interaction.ToolSetConfig{Tools: []tool.Tool{failing}, Observer: observer})
			events := &agenttest.ObservationRecorder{}
			engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: deployment.resolver, EventListeners: []agent.EventListener{events}})
			if err != nil {
				t.Fatal(err)
			}
			process, err := engine.Start(t.Context(), deployment.Deployment, interactionInput(t, "preserve the unknown result"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				if killErr := process.Kill(ctx, "release test tree"); killErr != nil && !errors.Is(killErr, agent.ErrProcessFinished) {
					t.Error(killErr)
				}
				if releaseErr := engine.ReleaseTree(ctx, process.ID()); releaseErr != nil {
					t.Error(releaseErr)
				}
				if closeErr := engine.Close(); closeErr != nil {
					t.Error(closeErr)
				}
			})
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			settled, err := events.AwaitEvent(ctx, func(event agent.Event) bool {
				fact, present := event.EffectFinished()
				return event.Name() == agent.EventEffectFinished && present && fact.SettlementStatus() == agent.SettlementStatusUnknown
			})
			if err != nil {
				t.Fatal(err)
			}
			fact, present := settled.EffectFinished()
			if !present || fact.SettlementStatus() != agent.SettlementStatusUnknown {
				t.Fatalf("Tool Effect settlement=%s, want unknown", fact.SettlementStatus())
			}
			select {
			case settlement := <-observer.settlements:
				if !settlement.Unknown || settlement.Failure == "" || settlement.Result != nil || settlement.InputRequired {
					t.Fatalf("Tool observation=%+v, want an unknown outcome diagnostic", settlement)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			toolProcess, found := engine.Process(settled.Relation().ProcessID())
			if !found {
				t.Fatal("unknown Tool Process is missing")
			}
			unknown := inspectProcessSnapshot(t, engine, toolProcess).UnknownEffectIDs()
			effectID, _ := settled.EffectID()
			if len(unknown) != 1 || unknown[0] != effectID {
				t.Fatalf("unknown Effects=%v", unknown)
			}
			if inspectProcessSnapshot(t, engine, process).Status().Terminal() || model.Calls() != 1 {
				t.Fatalf("status=%s model calls=%d", inspectProcessSnapshot(t, engine, process).Status(), model.Calls())
			}
			if killErr := process.Kill(ctx, "retain unresolved outcome in terminal result"); killErr != nil {
				t.Fatal(killErr)
			}
			result, err := process.Await(ctx)
			if err != nil || result.Status() != agent.StatusKilled || len(result.Termination().UnresolvedEffectIDs()) != 0 {
				t.Fatalf("root termination=%+v error=%v", result.Termination(), err)
			}
			toolResult, err := toolProcess.Await(ctx)
			unresolved := toolResult.Termination().UnresolvedEffectIDs()
			if err != nil || toolResult.Status() != agent.StatusCanceled || toolResult.Termination().Cause() != agent.TerminationCauseParentCancellation || len(unresolved) != 1 || unresolved[0] != effectID {
				t.Fatalf("Tool termination=%+v error=%v", toolResult.Termination(), err)
			}
		})
	}
}

type toolSettlementObserver struct {
	settlements chan interaction.ToolSettlement
}

func (*toolSettlementObserver) OnModelResponse(context.Context, interaction.ModelInvocation, *chat.Response) {
}
func (*toolSettlementObserver) OnToolStarted(context.Context, interaction.ToolInvocation) {}
func (t *toolSettlementObserver) OnToolSettled(_ context.Context, _ interaction.ToolInvocation, settlement interaction.ToolSettlement) {
	t.settlements <- settlement
}

func runInteraction(t *testing.T, deployment interactionDeployment, prompt string) agent.Result {
	t.Helper()
	engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: deployment.resolver})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(context.Background(), deployment.Deployment, interactionInput(t, prompt))
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	return result
}

func assertInteractionHostFailure(t *testing.T, result agent.Result) {
	t.Helper()
	failure, ok := result.Termination().Failure()
	if result.Status() != agent.StatusFailed || !ok ||
		failure.Kind() != agent.FailureKindExternal || failure.Code() != "interaction.host.failed" {
		t.Fatalf("result = status:%s termination:%#v", result.Status(), result.Termination())
	}
}

func TestDefinitionRejectsZeroModelCallLimit(t *testing.T) {
	_, err := interaction.NewDefinition(interaction.DefinitionConfig{
		Name: "interaction.test", Description: "Run a test interaction.",
	})
	if !errors.Is(err, interaction.ErrInvalidDefinitionConfig) {
		t.Fatalf("error = %v, want ErrInvalidDefinitionConfig", err)
	}
}

func TestDefinitionRestoresCompleteWorkingContext(t *testing.T) {
	definition, err := interaction.NewDefinition(interaction.DefinitionConfig{
		Name:          "interaction.restore",
		Description:   "Verify exact Interaction state restoration.",
		MaxModelCalls: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	input, err := agent.EncodeInput(interaction.Input{
		Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("persist me"))},
	})
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
	if transition.Kind() != agent.TransitionKindContinue || len(transition.Effects()) != 1 {
		t.Fatalf("transition = %#v", transition)
	}
	before, err := execution.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := definition.Restore(before)
	if err != nil {
		t.Fatal(err)
	}
	after, err := restored.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before.Payload(), after.Payload()) {
		t.Fatalf("restored payload differs\nbefore: %s\nafter:  %s", before.Payload(), after.Payload())
	}
}

func TestDirectResultToolCompletesWithoutAnotherModelCall(t *testing.T) {
	type input struct {
		Value string `json:"value"`
	}
	echo, err := tool.NewFunc(tool.FuncConfig{
		Name:        "echo",
		Description: "Return the supplied value directly.",
	}, func(_ context.Context, input input) (string, error) {
		return input.Value, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	model := &singleToolCallModel{call: chat.ToolCall{ID: "call_direct", Name: "echo", Arguments: `{"value":"direct"}`}}
	deployment := newDeployment(t, model, []tool.Tool{directTool{Tool: echo}}, 2)
	engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: deployment.resolver})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(context.Background(), deployment.Deployment, interactionInput(t, "direct"))
	if err != nil {
		t.Fatal(err)
	}
	if closeErr := engine.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if result.Status() != agent.StatusCompleted || model.Calls() != 1 {
		t.Fatalf("status = %s, model calls = %d", result.Status(), model.Calls())
	}
	erased, _ := result.Output()
	output, err := erased.Decode[interaction.Output]()
	if err != nil {
		t.Fatal(err)
	}
	if output.Source != interaction.CompletionSourceDirectToolResults || output.ModelResponse != nil ||
		len(output.DirectToolResults) != 1 {
		t.Fatalf("output = %#v", output)
	}
	directText, directOK := output.DirectToolResults[0].Output.Text()
	if !directOK || directText != "direct" {
		t.Fatalf("output = %#v", output)
	}
}

func TestModelCallLimitProducesStableFailure(t *testing.T) {
	type noInput struct{}
	next, err := tool.NewFunc(tool.FuncConfig{
		Name:        "next",
		Description: "Request another model round.",
	}, func(context.Context, noInput) (string, error) { return "continue", nil })
	if err != nil {
		t.Fatal(err)
	}
	model := &singleToolCallModel{call: chat.ToolCall{ID: "call_limit", Name: "next", Arguments: `{}`}}
	deployment := newDeployment(t, model, []tool.Tool{next}, 1)
	engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: deployment.resolver})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(context.Background(), deployment.Deployment, interactionInput(t, "loop"))
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	if result.Status() != agent.StatusFailed || model.Calls() != 1 {
		t.Fatalf("status = %s, model calls = %d", result.Status(), model.Calls())
	}
	failure, ok := result.Termination().Failure()
	if !ok || failure.Code() != "interaction.limit.model_calls" || failure.Kind() != agent.FailureKindExecution {
		t.Fatalf("failure = %#v, present = %t", failure, ok)
	}
}

type scriptedModel struct {
	mu    sync.Mutex
	calls int
}

type singleToolCallModel struct {
	mu    sync.Mutex
	calls int
	call  chat.ToolCall
}

func (s *singleToolCallModel) Call(context.Context, *chat.Request) (*chat.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return toolCallResponse(s.call), nil
}

func (s *singleToolCallModel) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type directTool struct {
	tool.Tool
}

func (d directTool) Unwrap() tool.Tool { return d.Tool }

func (directTool) ReturnsDirectResult() bool { return true }

func (s *scriptedModel) Call(_ context.Context, request *chat.Request) (*chat.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if len(request.Tools) != 1 || request.Tools[0].Name != "add" {
		return nil, errors.New("tool manifest is not frozen into model request")
	}
	if s.calls == 1 {
		if len(request.Messages) != 1 {
			return nil, errors.New("first request has unexpected WorkingContext")
		}
		return toolCallResponse(chat.ToolCall{ID: "call_1", Name: "add", Arguments: `{"left":2,"right":3}`}), nil
	}
	if len(request.Messages) != 3 || request.Messages[1].Role != chat.RoleAssistant || request.Messages[2].Role != chat.RoleTool {
		return nil, errors.New("tool continuation does not preserve model order")
	}
	result := request.Messages[2].Parts[0].ToolResult
	resultText, textOK := toolResultText(result)
	if result == nil || !textOK || result.ID != "call_1" || result.Name != "add" || resultText != "5" {
		return nil, errors.New("tool continuation contains the wrong result")
	}
	return textResponse("5"), nil
}

func (s *scriptedModel) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func newDeployment(t *testing.T, model chat.Model, tools []tool.Tool, maxModelCalls uint32) interactionDeployment {
	t.Helper()
	client, err := chatclient.New(model, chatclient.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return configuredInteraction(t, interaction.DefinitionConfig{
		Name: "interaction.test", Description: "Run a model-directed interaction for contract testing.", MaxModelCalls: maxModelCalls,
	}, interaction.DispatcherConfig{Client: client}, interaction.ToolSetConfig{Tools: tools})
}

func textResponse(text string) *chat.Response {
	message := chat.NewAssistantMessage(chat.NewTextPart(text))
	return &chat.Response{Output: &chat.Output{
		Message:      &message,
		FinishReason: chat.FinishReasonStop,
	}}
}

func toolCallResponse(call chat.ToolCall) *chat.Response {
	message := chat.NewAssistantMessage(chat.NewToolCallPart(call))
	return &chat.Response{Output: &chat.Output{
		Message:      &message,
		FinishReason: chat.FinishReasonToolCalls,
	}}
}
