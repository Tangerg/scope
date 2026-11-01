package agent_test

import (
	startrand "crypto/rand"

	"context"
	"encoding/json"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/core/tool"
)

func TestSteerQueuedDuringChildWaitSurvivesRestore(t *testing.T) {
	for _, readStore := range []bool{false, true} {
		name := "capture_tree"
		if readStore {
			name = "load_store"
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
			client, clientErr := chatclient.New(model, chatclient.Config{})
			if clientErr != nil {
				t.Fatal(clientErr)
			}
			deployment := configuredInteraction(t,
				agent.DefinitionConfig{Name: "interaction.unlimited_resume", Description: "Resume an unlimited input wait."},
				agent.DispatcherConfig{Model: client}, agent.ToolSetConfig{Tools: []tool.Tool{waiting}},
			)
			config := run.Config{Store: run.NewMemoryStore()}
			var store *run.MemoryStore
			if readStore {
				store = run.NewMemoryStore()
				config.Store = store
			}
			engine, err := run.New(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			root, err := engine.Start(t.Context(), deployment.Program, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: interactionInput(t, "greet")})
			if err != nil {
				t.Fatal(err)
			}
			loadSnapshot := func() (run.TreeSnapshot, error) {
				if store != nil {
					snapshot, _, loadErr := store.LoadTree(t.Context(), root.Relation().ProcessID())
					return snapshot, loadErr
				}
				return engine.CaptureTree(t.Context(), root.Relation().ProcessID())
			}
			var pending agent.PendingToolInput
			for !pending.Valid() {
				snapshot, loadErr := loadSnapshot()
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				waits, readErr := agent.PendingToolInputs(snapshot)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if len(waits) == 1 {
					pending = waits[0]
				}
				runtime.Gosched()
			}
			waitForStatus(t, engine, root, run.StatusWaiting)
			id, err := run.ParseSignalID("signal:queued-child-wait")
			if err != nil {
				t.Fatal(err)
			}
			steer, err := agent.NewSteerSignal(id, chat.NewUserMessage(chat.NewTextPart("retain this steer")))
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
			if inspectProcessSnapshot(t, engine, root).Status() != run.StatusWaiting || modelCalls.Load() != 1 || waiting.continuationCalls.Load() != 0 {
				t.Fatal("queued Strategy input released the child wait")
			}
			restoredEngine, err := run.New(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := restoredEngine.Recover(t.Context(), deployment.Program, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if accepted, deliverErr := restored.DeliverSignals(t.Context(), steer); deliverErr != nil || accepted {
				t.Fatalf("restored duplicate steer=%t error=%v", accepted, deliverErr)
			}
			owner := pendingToolProcess(t, restoredEngine, pending)
			answerID, err := run.ParseSignalID("signal:queued-steer-answer")
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
			if err != nil || result.Termination().Status() != run.StatusCompleted || modelCalls.Load() != 2 || waiting.initialCalls.Load() != 1 || waiting.continuationCalls.Load() != 1 {
				t.Fatalf("restored status=%s error=%v model/tool calls=%d/%d/%d", result.Termination().Status(), err, modelCalls.Load(), waiting.initialCalls.Load(), waiting.continuationCalls.Load())
			}
			if closeErr := restoredEngine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
				t.Fatal(closeErr)
			}
			if killErr := root.Kill(t.Context(), "release old waiting tree"); killErr != nil {
				t.Fatal(killErr)
			}
			for _, captured := range snapshot.ProcessSnapshots() {
				process, exists := engine.Process(captured.Relation().ProcessID())
				if !exists {
					t.Fatal("old Process is missing")
				}
				_, awaitErr := process.Await(t.Context())
				if _, fenced := errors.AsType[*run.RuntimeError](awaitErr); awaitErr != nil && !fenced {
					t.Fatal(awaitErr)
				}
			}
			if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
				t.Fatal(closeErr)
			}
		})
	}
}

func TestSteerAdmittedDuringWaitStepSurvivesToolInput(t *testing.T) {
	steerID, err := run.ParseSignalID("signal:steer-during-wait-step")
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
		invocation, ok := agent.ModelInvocationFromContext(ctx)
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
	toolSet := testToolSet(t, agent.ToolSetConfig{Tools: []tool.Tool{waiting}})
	definition, err := agent.NewDefinition(agent.DefinitionConfig{
		Name: "interaction.steer_wait", Description: "Preserve accepted steering across an input wait.", MaxModelCalls: run.NewQuota(2), Tools: toolSet, ToolBudget: run.Budget{Steps: run.NewQuota(16), Effects: run.NewQuota(8), Signals: run.NewQuota(16)},
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := agent.NewDispatcher(definition, agent.DispatcherConfig{
		Model: model,
	})
	if err != nil {
		t.Fatal(err)
	}
	gate := &waitingStepDefinition{Definition: definition, entered: make(chan struct{}), release: newToolRelease()}
	deployment, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{
		Definition: gate, Dispatcher: dispatcher,
		ImplementationDigest: run.ComputeDigest([]byte("steer-wait-step")),
		ConfigurationDigest:  run.ComputeDigest([]byte("steer-wait-input")),
	})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		gate.release.Release()
		if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	process, err := engine.Start(t.Context(), deployment, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: interactionInput(t, "greet the user")})
	if err != nil {
		t.Fatal(err)
	}
	<-gate.entered
	steer, err := agent.NewSteerSignal(steerID, chat.NewUserMessage(chat.NewTextPart("include the greeting")))
	if err != nil {
		t.Fatal(err)
	}
	if accepted, deliverErr := process.DeliverSignals(t.Context(), steer); deliverErr != nil || !accepted {
		t.Fatalf("DeliverSignals = %t, %v", accepted, deliverErr)
	}
	gate.release.Release()
	_, pending := captureToolInput(t, engine, process)
	toolProcess := pendingToolProcess(t, engine, pending)
	answerID, err := run.ParseSignalID("signal:steered-input-answer")
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
	if err != nil || result.Termination().Status() != run.StatusCompleted {
		t.Fatalf("result = %s, %#v, %v", result.Termination().Status(), result.Termination(), err)
	}
	if modelCalls.Load() != 2 || waiting.initialCalls.Load() != 1 || waiting.continuationCalls.Load() != 1 {
		t.Fatalf("model calls = %d, Tool initial/continuation = %d/%d", modelCalls.Load(), waiting.initialCalls.Load(), waiting.continuationCalls.Load())
	}
}

// Hold the wait Step after its input window is fixed, while the Process still
// admits unaddressed Signals for the next Step.
type waitingStepDefinition struct {
	run.Definition
	entered chan struct{}
	release *toolRelease
	once    sync.Once
}

func (w *waitingStepDefinition) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	execution, err := w.Definition.Restore(ctx, state)
	if err != nil {
		return nil, err
	}
	return &waitingStepExecution{Execution: execution, definition: w}, nil
}

type waitingStepExecution struct {
	run.Execution
	definition *waitingStepDefinition
}

func (w *waitingStepExecution) Step(ctx context.Context, signals []run.Signal) (run.Transition, error) {
	transition, err := w.Execution.Step(ctx, signals)
	if err == nil && transition.Kind() == run.TransitionKindWait {
		w.definition.once.Do(func() { close(w.definition.entered) })
		select {
		case <-w.definition.release.done:
		case <-ctx.Done():
			return run.Transition{}, ctx.Err()
		}
	}
	return transition, err
}
