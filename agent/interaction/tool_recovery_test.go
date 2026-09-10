package interaction_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestToolRecoveryPreservesIndependentSettlementsAfterLostAcknowledgment(t *testing.T) {
	for _, concurrency := range []int{1, 2} {
		t.Run(fmt.Sprintf("concurrency_%d", concurrency), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			store := agenttest.NewMemoryTreeDurability()
			gate := &toolSettlementCrash{MemoryTreeDurability: store, firstSettled: make(chan struct{})}
			first := &recoveryTool{name: "first", key: "first"}
			uncertain := &recoveryTool{name: "uncertain", key: "shared", after: gate.firstSettled, unknown: true}
			last := &recoveryTool{name: "last", key: "shared"}
			var modelCalls atomic.Int32
			model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
				if modelCalls.Add(1) == 1 {
					return toolBatchResponse(
						chat.ToolCall{ID: "call_first", Name: first.name, Arguments: `{}`},
						chat.ToolCall{ID: "call_uncertain", Name: uncertain.name, Arguments: `{}`},
						chat.ToolCall{ID: "call_last", Name: last.name, Arguments: `{}`},
					), nil
				}
				message := request.Messages[len(request.Messages)-1]
				if message.Role != chat.RoleTool || len(message.Parts) != 3 {
					return nil, errors.New("missing ordered Tool results")
				}
				for index, name := range []string{"first", "uncertain", "last"} {
					result := message.Parts[index].ToolResult
					if result == nil || result.Name != name || result.ID != "call_"+name || result.IsError {
						return nil, errors.New("Tool result order or identity changed during recovery")
					}
					wantText := name
					if name == "uncertain" {
						wantText = "resolved"
					}
					if len(result.Output.Content) != 1 || result.Output.Content[0].Kind != chat.PartText || result.Output.Content[0].Text != wantText {
						return nil, fmt.Errorf("Tool %s recovered content = %+v, want %q", name, result.Output.Content, wantText)
					}
				}
				return textResponse("recovered"), nil
			})
			deployment := configuredInteraction(t, interaction.DefinitionConfig{
				Name: "interaction.tool-recovery", Description: "Recover individual Tool effects.",
				MaxModelCalls: 2, MaxConcurrentToolCalls: concurrency,
			}, interaction.DispatcherConfig{Model: model}, interaction.ToolSetConfig{Tools: []tool.Tool{first, uncertain, last}})
			engine, err := agent.NewEngine(agent.EngineConfig{TreeDurability: gate, DeploymentResolver: deployment.resolver})
			if err != nil {
				t.Fatal(err)
			}
			root, err := engine.Start(ctx, deployment.Deployment, interactionInput(t, "recover"))
			if err != nil {
				t.Fatal(err)
			}
			failed, err := root.Await(ctx)
			if _, stopped := errors.AsType[*agent.RuntimeError](err); !stopped || failed.Valid() {
				t.Fatalf("lost acknowledgment status=%s error=%v", failed.Status(), err)
			}
			head, found, err := store.LoadTree(ctx, root.ID())
			if err != nil || !found {
				t.Fatalf("load committed cut=%t error=%v", found, err)
			}
			if first.calls.Load() != 1 || uncertain.calls.Load() != 1 || last.calls.Load() != 0 || len(head.ProcessSnapshots()) != 3 {
				t.Fatalf("calls before recovery=%d/%d/%d processes=%d", first.calls.Load(), uncertain.calls.Load(), last.calls.Load(), len(head.ProcessSnapshots()))
			}
			if !gate.firstRequest.Valid() || !gate.unknownRequest.Valid() || gate.firstRequest.ID() == gate.unknownRequest.ID() || gate.firstRequest.ProcessID() == gate.unknownRequest.ProcessID() {
				t.Fatal("Tool attempts did not retain independent Effect and Process identities")
			}
			for _, snapshot := range head.ProcessSnapshots() {
				process, exists := engine.Process(snapshot.ProcessID())
				if !exists {
					t.Fatal("old Process is missing")
				}
				if _, awaitErr := process.Await(ctx); awaitErr != nil {
					if _, stopped := errors.AsType[*agent.RuntimeError](awaitErr); !stopped {
						t.Fatal(awaitErr)
					}
				}
			}
			if releaseErr := engine.ReleaseTree(ctx, root.ID()); releaseErr != nil {
				t.Fatal(releaseErr)
			}
			if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
				t.Fatal(closeErr)
			}
			restoredEngine, err := agent.NewEngine(agent.EngineConfig{TreeDurability: store, DeploymentResolver: deployment.resolver})
			if err != nil {
				t.Fatal(err)
			}
			restored, err := restoredEngine.RestoreTree(ctx, deployment.Deployment, head)
			if err != nil {
				t.Fatal(err)
			}
			owner, exists := restoredEngine.Process(gate.unknownRequest.ProcessID())
			if !exists {
				t.Fatal("unknown Tool owner was not restored")
			}
			unknown := inspectProcessSnapshot(t, restoredEngine, owner).UnknownEffectIDs()
			if len(unknown) != 1 || unknown[0] != gate.unknownRequest.ID() {
				t.Fatalf("restored unknown identities=%v", unknown)
			}
			if first.calls.Load() != 1 || uncertain.calls.Load() != 1 || last.calls.Load() != 0 {
				t.Fatal("recovery replayed an established or unknown Tool attempt")
			}
			resolution, err := agent.NewSettlement(unknown[0], agent.SettlementStatusSucceeded, json.RawMessage(`{"operation":"tool_call","tool_result":{"result":{"id":"call_uncertain","name":"uncertain","output":{"content":[{"kind":"text","text":"resolved"}]}},"direct":false}}`))
			if err != nil {
				t.Fatal(err)
			}
			if resolveErr := owner.ResolveUnknownEffect(ctx, resolution); resolveErr != nil {
				t.Fatal(resolveErr)
			}
			result, err := restored.Await(ctx)
			if err != nil || result.Status() != agent.StatusCompleted {
				t.Fatalf("restored result=%s termination=%v error=%v", result.Status(), result.Termination(), err)
			}
			if first.calls.Load() != 1 || uncertain.calls.Load() != 1 || last.calls.Load() != 1 || modelCalls.Load() != 2 {
				t.Fatalf("final tool/model calls=%d/%d/%d/%d", first.calls.Load(), uncertain.calls.Load(), last.calls.Load(), modelCalls.Load())
			}
			if closeErr := restoredEngine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
				t.Fatal(closeErr)
			}
		})
	}
}

type recoveryTool struct {
	name, key string
	unknown   bool
	after     <-chan struct{}
	calls     atomic.Int32
}

func (r *recoveryTool) Definition() chat.ToolDefinition {
	return chat.ToolDefinition{Name: r.name, Description: "Exercise independent recovery.", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`)}
}

func (r *recoveryTool) ConcurrencyKey(tool.Invocation) (string, bool) { return r.key, true }

func (r *recoveryTool) Call(ctx context.Context, _ tool.Invocation) (chat.ToolOutput, error) {
	r.calls.Add(1)
	if r.after != nil {
		select {
		case <-r.after:
		case <-ctx.Done():
			return chat.ToolOutput{}, ctx.Err()
		}
	}
	if r.unknown {
		return chat.ToolOutput{}, interaction.HostFailure(errors.New("external outcome unavailable"))
	}
	return chat.NewTextToolOutput(r.name), nil
}

type toolSettlementCrash struct {
	*agenttest.MemoryTreeDurability
	firstSettled   chan struct{}
	firstRequest   agent.EffectRequest
	unknownRequest agent.EffectRequest
}

func (t *toolSettlementCrash) CommitEffect(ctx context.Context, boundary agent.EffectBoundary) error {
	if err := t.MemoryTreeDurability.CommitEffect(ctx, boundary); err != nil {
		return err
	}
	settlement, present := boundary.Settlement()
	if !present {
		return nil
	}
	var envelope struct {
		ToolCall *struct {
			Call chat.ToolCall `json:"call"`
		} `json:"tool_call"`
	}
	if err := json.Unmarshal(boundary.Request().Effect().Payload(), &envelope); err != nil {
		return err
	}
	if envelope.ToolCall != nil && envelope.ToolCall.Call.Name == "first" {
		t.firstRequest = boundary.Request()
		close(t.firstSettled)
	}
	if settlement.Status() == agent.SettlementStatusUnknown {
		t.unknownRequest = boundary.Request()
		return errors.New("crash after committed Tool settlement before acknowledgment")
	}
	return nil
}
