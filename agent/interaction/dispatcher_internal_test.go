package interaction

import (
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/Tangerg/scope/core/chat"
)

func TestFailedDirectResultCannotEnterProtocolOrRestore(t *testing.T) {
	result := chat.ToolResult{
		ID: "failed", Name: "direct", IsError: true, Output: chat.NewTextToolOutput("failure"),
	}
	payload, err := encodeProtocol(signalEnvelope{
		Operation:  operationToolBatch,
		ToolResult: &toolBatchResult{Results: []chat.ToolResult{result}, Direct: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, decodeErr := decodeSignal(payload); decodeErr == nil {
		t.Fatal("failed direct result entered the tool protocol")
	}
	definition, err := NewDefinition(DefinitionConfig{
		Name: "interaction.restore_direct", Description: "Validate completed direct result recovery.", MaxModelCalls: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	state := executionState{
		Phase: phaseCompleted, ModelCallCount: 1,
		WorkingContext: &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("run"))}},
		FinalOutput: &Output{
			Source: CompletionSourceDirectToolResults, ModelCalls: 1, DirectToolResults: []chat.ToolResult{result},
		},
	}
	encoded, err := encodeState(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, restoreErr := definition.Restore(encoded); !errors.Is(restoreErr, ErrInvalidExecutionState) {
		t.Fatalf("Restore = %v, want ErrInvalidExecutionState", restoreErr)
	}
	state.FinalOutput.DirectToolResults[0].IsError = false
	encoded, err = encodeState(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, restoreErr := definition.Restore(encoded); restoreErr != nil {
		t.Fatalf("Restore successful direct result: %v", restoreErr)
	}
}

func TestModelHostFailureSignalModesAreExclusive(t *testing.T) {
	modelHost := signalEnvelope{
		Operation:   operationModelCall,
		ModelResult: &modelCallResult{HostError: "journal unavailable"},
	}
	if err := modelHost.validate(); err != nil {
		t.Fatalf("model host failure: %v", err)
	}
	response := chat.Response{}
	modelHost.ModelResult.Response = &response
	modelHost.ModelResult.EffectiveMessages = []chat.Message{
		chat.NewUserMessage(chat.NewTextPart("must not accompany host failure")),
	}
	if err := modelHost.validate(); err == nil {
		t.Fatal("model result combined a host failure with a response")
	}
}

func TestToolBatchPauseCountDoesNotWrap(t *testing.T) {
	request, err := NewToolInputRequest(
		json.RawMessage(`"provide another value"`),
		json.RawMessage(`{"type":"string"}`),
		json.RawMessage(`{"continuation":true}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	dispatch := toolBatchDispatch{pauseCount: math.MaxUint32}
	if _, err := dispatch.pause(0, request); err == nil {
		t.Fatal("exhausted Tool input pause count wrapped instead of failing")
	}
	if dispatch.pauseCount != math.MaxUint32 {
		t.Fatalf("pause count changed to %d", dispatch.pauseCount)
	}
}
