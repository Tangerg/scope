package interaction

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/core/chat"
)

func TestArtifactStateRestoreRejectsInvalidProvenanceAndValue(t *testing.T) {
	definition := fuzzInteractionDefinition(t)
	request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("validate"))}}
	validOutput, _ := agent.EncodePayload(fuzzDelegateOutput{Result: "valid"})
	wrongOutput, _ := agent.EncodePayload(struct {
		Other string `json:"other"`
	}{Other: "invalid"})

	cases := []struct {
		name      string
		artifacts []artifactRecord
	}{
		{
			name: "unknown Delegate",
			artifacts: []artifactRecord{{
				ModelCallSequence: 1, ToolCallIndex: 0, ToolCallID: "call_1",
				DelegateName: "missing", Output: validOutput,
			}},
		},
		{
			name: "wrong schema",
			artifacts: []artifactRecord{{
				ModelCallSequence: 1, ToolCallIndex: 0, ToolCallID: "call_1",
				DelegateName: "delegate_fuzz", Output: wrongOutput,
			}},
		},
		{
			name: "duplicate identity",
			artifacts: []artifactRecord{
				{ModelCallSequence: 1, ToolCallIndex: 0, ToolCallID: "same", DelegateName: "delegate_fuzz", Output: validOutput},
				{ModelCallSequence: 1, ToolCallIndex: 1, ToolCallID: "same", DelegateName: "delegate_fuzz", Output: validOutput},
			},
		},
		{
			name: "reversed position",
			artifacts: []artifactRecord{
				{ModelCallSequence: 1, ToolCallIndex: 1, ToolCallID: "later", DelegateName: "delegate_fuzz", Output: validOutput},
				{ModelCallSequence: 1, ToolCallIndex: 0, ToolCallID: "earlier", DelegateName: "delegate_fuzz", Output: validOutput},
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			state := executionState{
				Phase: phaseAwaitingModel, WorkingContext: request.Clone(), ModelCallCount: 2,
				ArtifactRecords: test.artifacts,
			}
			payload, err := jsonv2.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			envelope, err := agent.ParseExecutionState(executionStateKind, payload)
			if err != nil {
				t.Fatal(err)
			}
			_, err = definition.Restore(t.Context(), envelope)
			if test.name == "wrong schema" && !errors.Is(err, agent.ErrSchemaValidation) {
				t.Fatalf("schema cause lost: %v", err)
			}
			if !errors.Is(err, ErrInvalidExecutionState) {
				t.Fatalf("Restore error=%v, want ErrInvalidExecutionState", err)
			}
		})
	}
}

func TestRestoreStopsBetweenArtifacts(t *testing.T) {
	definition := fuzzInteractionDefinition(t)
	output, err := agent.EncodePayload(fuzzDelegateOutput{Result: "valid"})
	if err != nil {
		t.Fatal(err)
	}
	state := executionState{ModelCallCount: 1, ArtifactRecords: []artifactRecord{
		{ModelCallSequence: 1, ToolCallIndex: 0, ToolCallID: "call_1", DelegateName: "delegate_fuzz", Output: output}, {},
	}}
	ctx, cancel := conformancetest.CancelAfterCheck(t.Context(), 2)
	defer cancel()
	if err := state.validateArtifacts(ctx, definition); !errors.Is(err, context.Canceled) {
		t.Fatalf("artifact validation = %v, want cancellation before malformed second artifact", err)
	}
}

func TestArtifactIdentitySurvivesRestoreWithoutCallHistory(t *testing.T) {
	definition := fuzzInteractionDefinition(t)
	output, err := agent.EncodePayload(fuzzDelegateOutput{Result: "valid"})
	if err != nil {
		t.Fatal(err)
	}
	state := executionState{
		Phase: phaseAwaitingModel, ModelCallCount: 3,
		WorkingContext: &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("reduced context"))}},
		ArtifactRecords: []artifactRecord{
			{ModelCallSequence: 1, ToolCallIndex: 0, ToolCallID: "reused", DelegateName: "delegate_fuzz", Output: output},
			{ModelCallSequence: 2, ToolCallIndex: 0, ToolCallID: "reused", DelegateName: "delegate_fuzz", Output: output},
		},
	}
	envelope, err := agent.EncodeExecutionState(executionStateKind, state)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := definition.Restore(t.Context(), envelope)
	if err != nil {
		t.Fatal(err)
	}
	again, err := restored.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := again.Decode[executionState](executionStateKind)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := newArtifacts(decoded.ArtifactRecords)
	if len(artifacts) != 2 {
		t.Fatalf("artifacts=%d", len(artifacts))
	}
	for index, artifact := range artifacts {
		if artifact.ModelCallSequence() != uint64(index+1) || artifact.ToolCallID() != "reused" || artifact.DelegateName() != "delegate_fuzz" {
			t.Fatalf("provenance=%+v", artifact)
		}
	}
}

func TestChildSignalFailuresPreserveProtocolCause(t *testing.T) {
	signals := []agent.Signal{{}}
	_, _, _, startErr := collectChildStarts(signals)
	_, _, _, openedErr := collectChildWaitOpened(signals)
	_, _, _, completedErr := collectChildWaitSatisfied(signals)
	for _, test := range []struct{ err, cause error }{{startErr, agent.ErrInvalidSignal}, {openedErr, agent.ErrInvalidChildWait}, {completedErr, agent.ErrInvalidChildWait}} {
		if !errors.Is(test.err, ErrInvalidExecutionState) || !errors.Is(test.err, test.cause) {
			t.Fatalf("protocol cause lost: %v", test.err)
		}
	}
}
