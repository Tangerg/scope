package interaction

import (
	"context"
	"testing"

	"github.com/Tangerg/scope/core/chat"
)

type fixtureMutatingObserver struct{}

func (fixtureMutatingObserver) OnModelResponse(context.Context, ModelInvocation, *chat.Response) {}
func (fixtureMutatingObserver) OnToolStarted(context.Context, ToolInvocation)                    {}
func (fixtureMutatingObserver) OnToolSettled(_ context.Context, _ ToolInvocation, s ToolSettlement) {
	s.Result.Output.Content[0].Text = "mutated"
	s.Result.Output.Details[0] = '['
}
func TestObserverResultIsolation(t *testing.T) {
	result := chat.ToolResult{ID: "call", Name: "tool", Output: chat.ToolOutput{Content: []chat.ToolContent{{Kind: chat.PartText, Text: "original"}}, Details: []byte(`{"value":1}`)}}
	dispatcher := &toolDispatcher{observer: fixtureMutatingObserver{}}
	dispatcher.observeToolSettled(t.Context(), ToolInvocation{}, ToolSettlement{Result: &result})
	if result.Output.Content[0].Text != "original" || string(result.Output.Details) != `{"value":1}` {
		t.Fatalf("observer mutated owned result: text=%q details=%s", result.Output.Content[0].Text, result.Output.Details)
	}
}
