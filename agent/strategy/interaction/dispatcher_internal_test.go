package interaction

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"math"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/core/chat"
)

func TestUnlimitedModelCallSequencePreservesIdentityAcrossNumericBoundaries(t *testing.T) {
	definition, err := NewDefinition(DefinitionConfig{Name: "interaction.counter", Description: "Check counter identity."})
	if err != nil {
		t.Fatal(err)
	}
	execution := &execution{definition: definition, state: executionState{
		ModelCallCount: math.MaxUint32,
		WorkingContext: &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("continue"))}},
	}}
	transition, err := execution.requestModel(0, nil)
	if err != nil || len(transition.Effects()) != 1 || execution.state.ModelCallCount != uint64(math.MaxUint32)+1 {
		t.Fatalf("32-bit boundary: count=%d error=%v", execution.state.ModelCallCount, err)
	}
	call := chat.ToolCall{ID: "call", Name: "tick", Arguments: `{}`}
	before, err := ToolChildKey(math.MaxUint32, call)
	if err != nil {
		t.Fatal(err)
	}
	after, err := ToolChildKey(execution.state.ModelCallCount, call)
	if err != nil || before == after {
		t.Fatalf("child identity reused: %v", err)
	}
	execution.state.ModelCallCount = math.MaxUint64
	if _, stepErr := execution.requestModel(0, nil); !errors.Is(stepErr, agent.ErrCounterExhausted) || execution.state.ModelCallCount != math.MaxUint64 {
		t.Fatalf("64-bit boundary wrapped: %v", stepErr)
	}
}

func TestToolDispatcherSettlesLocalProtocolRejections(t *testing.T) {
	model, err := newModelEffect(&chat.Request{
		Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("run"))},
	}, 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	modelPayload, err := jsonv2.Marshal(model, jsonv2.Deterministic(true))
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range [][]byte{[]byte(`null`), []byte(`{}`), modelPayload} {
		effect, effectErr := agent.NewDispatcherEffect(payload)
		if effectErr != nil {
			t.Fatal(effectErr)
		}
		conformancetest.CheckDispatcherRejection(t, &toolDispatcher{}, effect)
	}
}

func TestResultWithoutDispositionCannotEnterProtocol(t *testing.T) {
	payload, err := jsonv2.Marshal(signalEnvelope{
		ToolResult: &toolDispatchResult{Completion: &toolCallResult{Output: chat.NewTextToolOutput("failure")}},
	}, jsonv2.Deterministic(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, decodeErr := decodeSignal(payload); decodeErr == nil {
		t.Fatal("result without a disposition entered the tool protocol")
	}
}

func TestModelResultRequiresResponse(t *testing.T) {
	// A failed call settles Failed with its diagnostic; a model result is only
	// ever a successful response.
	if err := (signalEnvelope{ModelResult: &modelCallResult{}}).validate(); !errors.Is(err, ErrInvalidProtocol) {
		t.Fatalf("model result without a response = %v", err)
	}
	if _, err := decodeSignal(json.RawMessage(`{"model_result":{"response":null,"host_error":"journal unavailable"}}`)); !errors.Is(err, jsonv2.ErrUnknownName) {
		t.Fatalf("model result accepted a host failure member: %v", err)
	}
}

func TestModelResultRejectsProviderErrorMember(t *testing.T) {
	_, err := decodeSignal(json.RawMessage(`{"model_result":{"error":"provider unavailable"}}`))
	if !errors.Is(err, ErrInvalidProtocol) || !errors.Is(err, jsonv2.ErrUnknownName) {
		t.Fatalf("decodeSignal = %v, want ErrInvalidProtocol wrapping an unknown member", err)
	}
}
