package interaction

import (
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestModelEffectConstructionRejectsAmbiguousOrInvalidAuthority(t *testing.T) {
	request := Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("run"))}}
	id, err := run.ParseSignalID("signal:steer")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		call  modelCall
		other *toolDispatchRequest
	}{
		{"missing sequence", modelCall{Request: request}, nil},
		{"empty request", modelCall{ModelCallSequence: 1}, nil},
		{"invalid tool definition", modelCall{ModelCallSequence: 1, Request: request, Tools: []tool.Definition{{}}}, nil},
		{"duplicate steering identity", modelCall{ModelCallSequence: 1, Request: request, AppliedSteerSignalIDs: []run.SignalID{id, id}}, nil},
		{"missing steering identity", modelCall{ModelCallSequence: 1, Request: request, AppliedSteerSignalIDs: []run.SignalID{{}}}, nil},
		{"second operation", modelCall{ModelCallSequence: 1, Request: request}, &toolDispatchRequest{Invocation: toolCall{Call: chat.ToolCall{ID: "call", Name: "inspect", Arguments: `{}`}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, envelope := range []effectEnvelope{{ModelCall: &test.call, ToolCall: test.other}, {Preparation: &test.call, ToolCall: test.other}} {
				if _, err := envelope.effect(); !errors.Is(err, ErrInvalidProtocol) {
					t.Fatalf("invalid dispatch command accepted: %v", err)
				}
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
		if _, err := invocation.effect(); !errors.Is(err, ErrInvalidProtocol) {
			t.Fatalf("invalid continuation admitted: %+v", invocation)
		}
	}
}

func TestPreparedContextRejectsUnusableMessages(t *testing.T) {
	for _, replacement := range [][]chat.Message{{}, {{}}} {
		err := (preparedContext{Messages: replacement}).validate()
		if !errors.Is(err, ErrInvalidProtocol) {
			t.Fatalf("invalid replacement accepted: %+v, %v", replacement, err)
		}
	}
}

func TestToolSettlementRejectsMissingOrUnusableInputContract(t *testing.T) {
	for _, result := range []toolDispatchResult{{}, {InputRequest: new(tool.InputRequest)}} {
		if settlement, err := result.settlement(); !errors.Is(err, ErrInvalidProtocol) {
			t.Fatalf("invalid continuation settlement accepted: %+v, %v", settlement, err)
		}
	}
}
