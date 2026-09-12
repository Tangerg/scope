package interaction

import (
	"context"
	"errors"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

type advertisementRecoveryTool struct{ tool.Tool }

func (a advertisementRecoveryTool) ConcurrencyPolicy() func(tool.Invocation) (string, bool) {
	return func(tool.Invocation) (string, bool) { return "", true }
}

func TestRestoreRejectsFailedToolAdvertisements(t *testing.T) {
	ordinary, err := tool.NewFunc(tool.FuncConfig{Name: "initial", Description: "Exercise recovery."}, func(context.Context, struct{}) (string, error) { return "done", nil })
	if err != nil {
		t.Fatal(err)
	}
	deferred, err := tool.NewFunc(tool.FuncConfig{Name: "first", Description: "Deferred tool."}, func(context.Context, struct{}) (string, error) { return "done", nil })
	if err != nil {
		t.Fatal(err)
	}
	tools, err := NewToolSet(ToolSetConfig{Name: "audit.tools", Description: "Exercise recovery.", Tools: []tool.Tool{advertisementRecoveryTool{ordinary}}, DeferredTools: []tool.Tool{deferred}, ImplementationDigest: agent.ComputeDigest([]byte("audit")), ConfigurationDigest: agent.ComputeDigest([]byte("audit"))})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := NewDefinition(DefinitionConfig{Name: "audit.interaction", Description: "Exercise recovery.", MaxModelCalls: 2, Tools: tools, ToolBudget: agent.Budget{Steps: 10, Effects: 10, Signals: 10}, MaxConcurrentToolCalls: 2})
	if err != nil {
		t.Fatal(err)
	}
	calls := []chat.ToolCall{{ID: "failed", Name: "initial", Arguments: `{}`}, {ID: "pending", Name: "initial", Arguments: `{}`}}
	firstKey, err := toolChildKey(1, calls[0])
	if err != nil {
		t.Fatal(err)
	}
	secondKey, err := toolChildKey(1, calls[1])
	if err != nil {
		t.Fatal(err)
	}
	firstID, err := agent.ParseProcessID("process:first")
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := agent.ParseProcessID("process:second")
	if err != nil {
		t.Fatal(err)
	}
	waitID, err := agent.ParseWaitID("wait:audit")
	if err != nil {
		t.Fatal(err)
	}
	message := chat.NewAssistantMessage(chat.NewToolCallPart(calls[0]), chat.NewToolCallPart(calls[1]))
	state := executionState{
		Phase: phaseWaitingChildren, ModelCallCount: 1, WorkingContext: &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("work"))}},
		ToolRound: &toolCallRound{Response: &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonToolCalls}}, ChildBatch: &childCallBatch{
			Kind: childCallsTool, NextStartIndex: 2, WaitID: &waitID, Invocations: []childInvocationState{
				{ChildKey: &firstKey, ProcessID: &firstID, Result: &toolCallResult{Result: chat.ToolResult{ID: "failed", Name: "initial", IsError: true, Output: chat.NewTextToolOutput("failed")}, AdvertisedToolNames: []string{"first"}}},
				{ChildKey: &secondKey, ProcessID: &secondID},
			},
		}},
	}
	encoded, err := encodeState(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := definition.Restore(encoded); !errors.Is(err, ErrInvalidExecutionState) {
		t.Fatalf("Restore accepted a failed Tool advertisement: %v", err)
	}
}
