package interaction_test

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestModelSettlementCarriesOnlyChangedContext(t *testing.T) {
	for _, mode := range []string{"absent", "unchanged", "changed"} {
		t.Run(mode, func(t *testing.T) {
			_, payloads := measureInteractionContext(t, 1, mode)
			if len(payloads) != 2 {
				t.Fatalf("model settlements=%d, want 2", len(payloads))
			}
			for _, payload := range payloads {
				var result struct {
					ModelResult struct {
						ReplacementMessages []chat.Message `json:"replacement_messages"`
					} `json:"model_result"`
				}
				if err := jsonv2.Unmarshal(payload, &result); err != nil {
					t.Fatal(err)
				}
				if changed := result.ModelResult.ReplacementMessages != nil; changed != (mode == "changed") {
					t.Fatalf("context mode=%s carries replacement=%t", mode, changed)
				}
			}
		})
	}
}

func TestInteractionSnapshotRetainsOneCurrentContext(t *testing.T) {
	for _, mode := range []string{"absent", "changed"} {
		t.Run(mode, func(t *testing.T) {
			small, _ := measureInteractionContext(t, 16, mode)
			large, _ := measureInteractionContext(t, 64, mode)
			t.Logf("snapshot bytes for 16/64 tool rounds: %d/%d", small, large)
			if large >= 5*small {
				t.Fatalf("snapshot growth exceeds current context plus bounded receipts: %d -> %d", small, large)
			}
		})
	}
}

func measureInteractionContext(t *testing.T, rounds uint32, mode string) (int, []json.RawMessage) {
	t.Helper()
	var calls uint32
	ready := make(chan struct{})
	model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		calls++
		if calls > rounds {
			want := 1 + 2*int(rounds)
			if mode == "changed" {
				want += int(rounds) + 1
			}
			if len(request.Messages) != want {
				t.Errorf("restored model context has %d messages, want %d", len(request.Messages), want)
			}
			return textResponse("done"), nil
		}
		return toolCallResponse(chat.ToolCall{ID: fmt.Sprintf("call_%d", calls), Name: "context", Arguments: `{}`}), nil
	})
	executable, err := newContinuingTool(tool.FuncConfig{
		Name: "context", Description: "Return a fixed size context contribution.",
	}, func(ctx context.Context, callInput tool.Invocation) (tool.Result, error) {
		if calls == rounds {
			if _, resumed := callInput.Continuation(); !resumed {
				close(ready)
				return waitingInput(json.RawMessage(`"continue?"`), json.RawMessage(`{"type":"boolean"}`), json.RawMessage(`null`))
			}
		}
		return tool.Result{Output: chat.NewTextToolOutput(strings.Repeat("x", 1024))}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	toolSet := testToolSet(t, interaction.ToolSetConfig{Tools: []tool.Tool{executable}})
	definition, err := interaction.NewDefinition(interaction.DefinitionConfig{
		Name: "interaction.context-storage", Description: "Check retained model context.", MaxModelCalls: agent.NewQuota(uint64(rounds) + 1), Tools: toolSet, ToolBudget: agent.Budget{Steps: agent.NewQuota(8), Effects: agent.NewQuota(4), Signals: agent.NewQuota(8)},
	})
	if err != nil {
		t.Fatal(err)
	}
	config := interaction.DispatcherConfig{Model: model}
	if mode != "absent" {
		config.ModelContextReducer = storageContextReducer{changed: mode == "changed"}
	}
	dispatcher, err := interaction.NewDispatcher(definition, config)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &contextSettlementRecorder{next: dispatcher}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, Dispatcher: recorder,
		ImplementationDigest: agent.ComputeDigest([]byte("context-storage-implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte(fmt.Sprintf("%s:%d", mode, rounds))),
	})
	if err != nil {
		t.Fatal(err)
	}
	engineConfig := agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter()}
	engine, err := agent.NewEngine(engineConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	ctx := t.Context()
	process, err := engine.Start(ctx, deployment, interactionInput(t, "start"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	snapshot, _ := captureToolInput(t, engine, process)
	activeBytes := len(rootProcessSnapshot(snapshot).JSON())
	parsed, err := agent.ParseTreeSnapshot(snapshot.JSON())
	if err != nil {
		t.Fatalf("active snapshot failed validation: %v", err)
	}
	recovered, err := agent.NewEngine(engineConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := recovered.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	root, err := recovered.RestoreTree(ctx, deployment, parsed)
	if err != nil {
		t.Fatal(err)
	}
	if killErr := process.Kill(ctx, "retire old activation"); killErr != nil && !errors.Is(killErr, agent.ErrTreeIncarnationConflict) {
		t.Fatal(killErr)
	}
	if joinErr := process.Join(ctx); !errors.Is(joinErr, agent.ErrTreeIncarnationConflict) {
		t.Fatalf("old activation exit = %v", joinErr)
	}
	_, pending := captureToolInput(t, recovered, root)
	id, err := agent.ParseSignalID("signal:resume-context")
	if err != nil {
		t.Fatal(err)
	}
	answer, err := pending.ResponseSignal(id, json.RawMessage(`true`))
	if err != nil {
		t.Fatal(err)
	}
	child := pendingToolProcess(t, recovered, pending)
	if _, deliveryErr := child.DeliverSignals(ctx, answer); deliveryErr != nil {
		t.Fatal(deliveryErr)
	}
	result, err := root.Await(ctx)
	if err != nil || result.Termination().Status() != agent.StatusCompleted {
		t.Fatalf("restored interaction=%s, %v", result.Termination().Status(), err)
	}
	if joinErr := root.Join(ctx); joinErr != nil {
		t.Fatal(joinErr)
	}
	final, err := recovered.CaptureTree(ctx, root.Relation().ProcessID())
	if err != nil {
		t.Fatal(err)
	}
	if rounds >= 16 && len(rootProcessSnapshot(final).JSON()) >= activeBytes {
		t.Fatal("completion retained the active working context")
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return activeBytes, recorder.modelResults
}

type storageContextReducer struct{ changed bool }

func (s storageContextReducer) ReduceModelContext(_ context.Context, invocation interaction.ModelInvocation, request *chat.Request) ([]chat.Message, error) {
	if s.changed {
		return append(request.Messages, chat.NewUserMessage(chat.NewTextPart(fmt.Sprintf("round %d", invocation.ModelCallSequence())))), nil
	}
	return request.Messages, nil
}

type contextSettlementRecorder struct {
	next         agent.Dispatcher
	mu           sync.Mutex
	modelResults []json.RawMessage
}

func (c *contextSettlementRecorder) Policy(effect agent.Effect) agent.EffectPolicy {
	return c.next.Policy(effect)
}

func (c *contextSettlementRecorder) Dispatch(ctx context.Context, request agent.EffectRequest, emit agent.DeltaEmitter) (agent.Settlement, error) {
	settlement, err := c.next.Dispatch(ctx, request, emit)
	if err != nil {
		return settlement, err
	}
	var envelope struct {
		ModelResult json.RawMessage `json:"model_result"`
	}
	if decodeErr := jsonv2.Unmarshal(settlement.Payload(), &envelope); decodeErr != nil {
		return agent.Settlement{}, decodeErr
	}
	if len(envelope.ModelResult) != 0 {
		c.mu.Lock()
		c.modelResults = append(c.modelResults, settlement.Payload())
		c.mu.Unlock()
	}
	return settlement, nil
}
