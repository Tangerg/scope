package interaction_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/interaction"
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
				if err := json.Unmarshal(payload, &result); err != nil {
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
	model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		calls++
		if calls > rounds {
			return textResponse("done"), nil
		}
		return toolCallResponse(chat.ToolCall{ID: fmt.Sprintf("call_%d", calls), Name: "context", Arguments: `{}`}), nil
	})
	executable, err := tool.NewFunc(tool.FuncConfig{
		Name: "context", Description: "Return a fixed size context contribution.",
	}, func(context.Context, struct{}) (string, error) { return strings.Repeat("x", 1024), nil })
	if err != nil {
		t.Fatal(err)
	}
	definition, err := interaction.NewDefinition(interaction.DefinitionConfig{
		Name: "interaction.context-storage", Description: "Check retained model context.", MaxModelCalls: rounds + 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	config := interaction.DispatcherConfig{Client: model, Tools: []tool.Tool{executable}}
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
	engine, err := agent.NewEngine(agent.EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	result, err := engine.Run(t.Context(), deployment, interactionInput(t, "start"))
	if err != nil || result.Status() != agent.StatusCompleted {
		t.Fatalf("interaction result=%s error=%v", result.Status(), err)
	}
	snapshot, err := engine.CaptureTree(t.Context(), result.ProcessID())
	if err != nil {
		t.Fatal(err)
	}
	if _, parseErr := agent.ParseTreeSnapshot(snapshot.JSON()); parseErr != nil {
		t.Fatalf("compacted snapshot failed recovery validation: %v", parseErr)
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return len(snapshot.JSON()), recorder.modelResults
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

func (c *contextSettlementRecorder) ReplayPolicy(effect agent.Effect) agent.ReplayPolicy {
	return c.next.ReplayPolicy(effect)
}

func (c *contextSettlementRecorder) Dispatch(ctx context.Context, request agent.EffectRequest, emit agent.DeltaEmitter) (agent.Settlement, error) {
	settlement, err := c.next.Dispatch(ctx, request, emit)
	if err != nil {
		return settlement, err
	}
	var envelope struct {
		ModelResult json.RawMessage `json:"model_result"`
	}
	if decodeErr := json.Unmarshal(settlement.Payload(), &envelope); decodeErr != nil {
		return agent.Settlement{}, decodeErr
	}
	if len(envelope.ModelResult) != 0 {
		c.mu.Lock()
		c.modelResults = append(c.modelResults, settlement.Payload())
		c.mu.Unlock()
	}
	return settlement, nil
}
