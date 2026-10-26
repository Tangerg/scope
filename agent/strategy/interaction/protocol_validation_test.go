package interaction

import (
	"errors"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestModelEffectConstructionRejectsAmbiguousOrInvalidAuthority(t *testing.T) {
	request := &Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("run"))}}
	id, err := agent.ParseSignalID("signal:steer")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		request  *Input
		sequence uint64
		names    []string
		steer    []agent.SignalID
	}{
		{"missing request", nil, 1, nil, nil},
		{"missing sequence", request, 0, nil, nil},
		{"empty request", &Input{}, 1, nil, nil},
		{"duplicate advertisements", request, 1, []string{"inspect", "inspect"}, nil},
		{"empty advertisement", request, 1, []string{""}, nil},
		{"duplicate steering identity", request, 1, nil, []agent.SignalID{id, id}},
		{"missing steering identity", request, 1, nil, []agent.SignalID{{}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := newModelEffect(test.request, test.sequence, test.names, test.steer); !errors.Is(err, ErrInvalidProtocol) {
				t.Fatalf("invalid dispatch command accepted: %v", err)
			}
		})
	}
}

func TestToolEffectConstructionRequiresItsExactContinuationContract(t *testing.T) {
	call := toolCall{Call: chat.ToolCall{ID: "call", Name: "inspect", Arguments: `{}`}}
	request, err := newInputRequest([]byte(`"approve"`), []byte(`{"type":"boolean"}`), []byte(`null`))
	if err != nil {
		t.Fatal(err)
	}
	for _, invocation := range []toolDispatchRequest{
		{},
		{Invocation: call, Resume: &toolResume{InputResponse: []byte(`true`)}},
		{Invocation: call, Resume: &toolResume{InputRequest: request, InputResponse: []byte(`"yes"`)}},
	} {
		if _, err := newToolEffect(invocation); err == nil {
			t.Fatalf("invalid continuation admitted: %+v", invocation)
		}
	}
}

func TestModelSettlementRejectsUnusableContextReplacement(t *testing.T) {
	message := chat.NewAssistantMessage(chat.NewTextPart("done"))
	response := &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}}
	for _, replacement := range [][]chat.Message{{}, {{}}} {
		settlement, err := (modelCallResult{Response: response, ReplacementMessages: replacement}).settlement(agent.MaxPayloadBytes)
		if settlement.Valid() || !errors.Is(err, ErrInvalidProtocol) {
			t.Fatalf("invalid replacement accepted: %+v, %v", replacement, err)
		}
	}
}

func TestToolSettlementRejectsMissingOrUnusableInputContract(t *testing.T) {
	for _, result := range []toolDispatchResult{{}, {InputRequest: new(tool.InputRequest)}} {
		if settlement, err := result.settlement(); settlement.Valid() || !errors.Is(err, ErrInvalidProtocol) {
			t.Fatalf("invalid continuation settlement accepted: %+v, %v", settlement, err)
		}
	}
}
