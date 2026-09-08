package interaction_test

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestSteerAdmittedDuringWaitStepSurvivesToolInput(t *testing.T) {
	steerID, err := agent.ParseSignalID("signal:steer-during-wait-step")
	if err != nil {
		t.Fatal(err)
	}
	waiting := newInputRequestTool()
	waiting.Release()
	var modelCalls atomic.Int32
	model := chat.ModelFunc(func(ctx context.Context, request *chat.Request) (*chat.Response, error) {
		if modelCalls.Add(1) == 1 {
			return toolCallResponse(chat.ToolCall{ID: "ask-1", Name: "ask_name", Arguments: `{}`}), nil
		}
		invocation, ok := interaction.ModelInvocationFromContext(ctx)
		ids := invocation.AppliedSteerSignalIDs()
		if !ok || len(ids) != 1 || ids[0] != steerID {
			t.Errorf("applied steer identities = %v", ids)
		}
		messages := request.Messages
		if len(messages) != 4 || messages[2].Role != chat.RoleTool || messages[3].Text() != "include the greeting" {
			t.Errorf("continuation messages = %#v", messages)
		}
		return textResponse("steered greeting"), nil
	})
	definition, err := interaction.NewDefinition(interaction.DefinitionConfig{
		Name: "interaction.steer_wait", Description: "Preserve accepted steering across an input wait.", MaxModelCalls: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := interaction.NewDispatcher(definition, interaction.DispatcherConfig{
		Client: model, Tools: []tool.Tool{waiting},
	})
	if err != nil {
		t.Fatal(err)
	}
	gate := &waitingStepDefinition{Definition: definition, entered: make(chan struct{}), release: newToolRelease()}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: gate, Dispatcher: dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("steer-wait-step")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("steer-wait-input")),
	})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := agent.NewEngine(agent.EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		gate.release.Release()
		if closeErr := engine.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	process, err := engine.Start(t.Context(), deployment, interactionInput(t, "greet the user"))
	if err != nil {
		t.Fatal(err)
	}
	<-gate.entered
	steer, err := interaction.NewSteerSignal(steerID, chat.NewUserMessage(chat.NewTextPart("include the greeting")))
	if err != nil {
		t.Fatal(err)
	}
	if accepted, deliverErr := process.DeliverSignals(t.Context(), steer); deliverErr != nil || !accepted {
		t.Fatalf("DeliverSignals = %t, %v", accepted, deliverErr)
	}
	gate.release.Release()
	waitForStatus(t, process, agent.StatusWaiting)
	pending, found, err := interaction.PendingToolInputFromProcess(t.Context(), process)
	if err != nil || !found {
		t.Fatalf("pending Tool input = %t, %v", found, err)
	}
	answerID, err := agent.ParseSignalID("signal:steered-input-answer")
	if err != nil {
		t.Fatal(err)
	}
	answer, err := pending.ResponseSignal(answerID, json.RawMessage(`"Ada"`))
	if err != nil {
		t.Fatal(err)
	}
	if accepted, deliverErr := process.DeliverSignals(t.Context(), answer); deliverErr != nil || !accepted {
		t.Fatalf("DeliverSignals = %t, %v", accepted, deliverErr)
	}
	result, err := process.Await(t.Context())
	if err != nil || result.Status() != agent.StatusCompleted {
		t.Fatalf("result = %s, %#v, %v", result.Status(), result.Termination(), err)
	}
	if modelCalls.Load() != 2 || waiting.initialCalls.Load() != 1 || waiting.continuationCalls.Load() != 1 {
		t.Fatalf("model calls = %d, Tool initial/continuation = %d/%d", modelCalls.Load(), waiting.initialCalls.Load(), waiting.continuationCalls.Load())
	}
}

// Hold the wait Step after its input window is fixed, while the Process still
// admits unaddressed Signals for the next Step.
type waitingStepDefinition struct {
	agent.Definition
	entered chan struct{}
	release *toolRelease
	once    sync.Once
}

func (w *waitingStepDefinition) Restore(state agent.ExecutionState) (agent.Execution, error) {
	execution, err := w.Definition.Restore(state)
	if err != nil {
		return nil, err
	}
	return &waitingStepExecution{Execution: execution, definition: w}, nil
}

type waitingStepExecution struct {
	agent.Execution
	definition *waitingStepDefinition
}

func (w *waitingStepExecution) Step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	transition, err := w.Execution.Step(ctx, signals)
	if err == nil && transition.Kind() == agent.TransitionKindWait {
		w.definition.once.Do(func() { close(w.definition.entered) })
		select {
		case <-w.definition.release.done:
		case <-ctx.Done():
			return agent.Transition{}, ctx.Err()
		}
	}
	return transition, err
}
