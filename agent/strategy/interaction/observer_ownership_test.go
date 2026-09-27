package interaction

import (
	"context"
	"testing"

	"github.com/Tangerg/scope/core/chat"
)

type fixtureMutatingObserver struct{}

func (fixtureMutatingObserver) OnModelStarted(context.Context, ModelInvocation, *chat.Request)   {}
func (fixtureMutatingObserver) OnModelSettled(context.Context, ModelInvocation, ModelSettlement) {}
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

type mutatingModelObserver struct{}

func (mutatingModelObserver) OnModelStarted(_ context.Context, _ ModelInvocation, request *chat.Request) {
	request.Messages[0].Parts[0].Text = "observer edit"
}

func (mutatingModelObserver) OnModelSettled(_ context.Context, _ ModelInvocation, settlement ModelSettlement) {
	settlement.Response.Output.Message.Parts[0].Text = "observer edit"
}

func TestModelObserverCannotChangeRequestOrSettlement(t *testing.T) {
	request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("original question"))}}
	dispatcher := &Dispatcher{observer: mutatingModelObserver{}, model: chat.ModelFunc(func(_ context.Context, got *chat.Request) (*chat.Response, error) {
		if got.Messages[0].Parts[0].Text != "original question" {
			t.Fatal("observer changed the actual model request")
		}
		message := chat.NewAssistantMessage(chat.NewTextPart("original answer"))
		return &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}}, nil
	})}
	response, err := dispatcher.callObservedModel(t.Context(), ModelInvocation{}, request, nil, 1<<20)
	if err != nil || response.Text() != "original answer" || request.Messages[0].Parts[0].Text != "original question" {
		t.Fatalf("observer changed model boundary: response=%v error=%v", response, err)
	}
}

type mutatingEvidenceObserver struct{}

func (mutatingEvidenceObserver) OnToolStarted(context.Context, ToolInvocation) {}
func (mutatingEvidenceObserver) OnToolSettled(_ context.Context, _ ToolInvocation, settlement ToolSettlement) {
	settlement.Evidence.Content[0].Text = "observer edit"
}

func TestToolObserverCannotChangeUnknownEvidence(t *testing.T) {
	evidence := chat.NewTextToolOutput("partial output")
	dispatcher := &toolDispatcher{observer: mutatingEvidenceObserver{}}
	dispatcher.observeToolSettled(t.Context(), ToolInvocation{}, ToolSettlement{Unknown: true, Evidence: &evidence})
	if evidence.Content[0].Text != "partial output" {
		t.Fatal("observer changed non-final evidence")
	}
}
