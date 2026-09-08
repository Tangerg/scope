package interaction_test

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestSteerQueuedDuringChildWaitSurvivesRestore(t *testing.T) {
	for _, durable := range []bool{false, true} {
		name := "ephemeral"
		if durable {
			name = "durable"
		}
		t.Run(name, func(t *testing.T) {
			waiting := newInputRequestTool()
			waiting.Release()
			var modelCalls atomic.Int32
			model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
				if modelCalls.Add(1) == 1 {
					return toolCallResponse(chat.ToolCall{ID: "ask", Name: "ask_name", Arguments: `{}`}), nil
				}
				messages := request.Messages
				if len(messages) != 4 || messages[2].Role != chat.RoleTool || messages[3].Text() != "retain this steer" {
					t.Errorf("recovered messages=%#v", messages)
				}
				return textResponse("done"), nil
			})
			deployment := newDeployment(t, model, []tool.Tool{waiting}, 2)
			config := agent.EngineConfig{DeploymentResolver: deployment.resolver}
			var store *agenttest.MemoryTreeDurability
			if durable {
				store = agenttest.NewMemoryTreeDurability()
				config.TreeDurability = store
			}
			engine, err := agent.NewEngine(config)
			if err != nil {
				t.Fatal(err)
			}
			root, err := engine.Start(t.Context(), deployment.Deployment, interactionInput(t, "greet"))
			if err != nil {
				t.Fatal(err)
			}
			loadSnapshot := func() (agent.TreeSnapshot, error) {
				if store != nil {
					snapshot, _, loadErr := store.LoadTree(t.Context(), root.ID())
					return snapshot, loadErr
				}
				return engine.CaptureTree(t.Context(), root.ID())
			}
			var pending interaction.PendingToolInput
			for !pending.Valid() {
				snapshot, loadErr := loadSnapshot()
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				waits, readErr := interaction.PendingToolInputs(snapshot)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if len(waits) == 1 {
					pending = waits[0]
				}
				runtime.Gosched()
			}
			waitForStatus(t, engine, root, agent.StatusWaiting)
			id, err := agent.ParseSignalID("signal:queued-child-wait")
			if err != nil {
				t.Fatal(err)
			}
			steer, err := interaction.NewSteerSignal(id, chat.NewUserMessage(chat.NewTextPart("retain this steer")))
			if err != nil {
				t.Fatal(err)
			}
			if accepted, deliverErr := root.DeliverSignals(t.Context(), steer); deliverErr != nil || !accepted {
				t.Fatalf("queue steer=%t error=%v", accepted, deliverErr)
			}
			snapshot, err := loadSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			if inspectProcessSnapshot(t, engine, root).Status() != agent.StatusWaiting || modelCalls.Load() != 1 || waiting.continuationCalls.Load() != 0 {
				t.Fatal("queued Strategy input released the child wait")
			}
			restoredEngine, err := agent.NewEngine(config)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := restoredEngine.RestoreTree(t.Context(), deployment.Deployment, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if accepted, deliverErr := restored.DeliverSignals(t.Context(), steer); deliverErr != nil || accepted {
				t.Fatalf("restored duplicate steer=%t error=%v", accepted, deliverErr)
			}
			owner := pendingToolProcess(t, restoredEngine, pending)
			answerID, err := agent.ParseSignalID("signal:queued-steer-answer")
			if err != nil {
				t.Fatal(err)
			}
			answer, err := pending.ResponseSignal(answerID, json.RawMessage(`"Ada"`))
			if err != nil {
				t.Fatal(err)
			}
			if accepted, deliverErr := owner.DeliverSignals(t.Context(), answer); deliverErr != nil || !accepted {
				t.Fatalf("answer=%t error=%v", accepted, deliverErr)
			}
			result, err := restored.Await(t.Context())
			if err != nil || result.Status() != agent.StatusCompleted || modelCalls.Load() != 2 || waiting.initialCalls.Load() != 1 || waiting.continuationCalls.Load() != 1 {
				t.Fatalf("restored status=%s error=%v model/tool calls=%d/%d/%d", result.Status(), err, modelCalls.Load(), waiting.initialCalls.Load(), waiting.continuationCalls.Load())
			}
			if closeErr := restoredEngine.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			if killErr := root.Kill(t.Context(), "release old waiting tree"); killErr != nil {
				t.Fatal(killErr)
			}
			for _, captured := range snapshot.ProcessSnapshots() {
				process, exists := engine.Process(captured.ProcessID())
				if !exists {
					t.Fatal("old Process is missing")
				}
				_, awaitErr := process.Await(t.Context())
				if _, fenced := errors.AsType[*agent.RuntimeError](awaitErr); awaitErr != nil && (!durable || !fenced) {
					t.Fatal(awaitErr)
				}
			}
			if closeErr := engine.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
		})
	}
}

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
	toolSet := testToolSet(t, interaction.ToolSetConfig{Tools: []tool.Tool{waiting}})
	definition, err := interaction.NewDefinition(interaction.DefinitionConfig{
		Name: "interaction.steer_wait", Description: "Preserve accepted steering across an input wait.", MaxModelCalls: 2, Tools: toolSet, ToolBudget: agent.Budget{Steps: 16, Effects: 8, Signals: 16},
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := interaction.NewDispatcher(definition, interaction.DispatcherConfig{
		Client: model,
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
	engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: toolInteractionDeployment(deployment, toolSet).resolver})
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
	_, pending := captureToolInput(t, engine, process)
	toolProcess := pendingToolProcess(t, engine, pending)
	answerID, err := agent.ParseSignalID("signal:steered-input-answer")
	if err != nil {
		t.Fatal(err)
	}
	answer, err := pending.ResponseSignal(answerID, json.RawMessage(`"Ada"`))
	if err != nil {
		t.Fatal(err)
	}
	if accepted, deliverErr := toolProcess.DeliverSignals(t.Context(), answer); deliverErr != nil || !accepted {
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
