package interaction_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

type unexecutedTool struct{ t *testing.T }

func (u unexecutedTool) Definition() tool.Definition {
	return tool.Definition{Effect: tool.EffectUnsafe, Name: "write", Description: "Write a record.", InputSchema: json.RawMessage(`{"type":"object"}`)}
}

func (u unexecutedTool) Call(context.Context, tool.Invocation) (tool.Result, error) {
	u.t.Error("a rejected model proposal executed a Tool")
	return tool.Result{}, nil
}

func rejectedModelResponse(t *testing.T, response *chat.Response) (run.Result[interaction.Output], run.Log) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	runtime, err := run.New(ctx, run.Config{Store: run.NewMemoryStore(), Build: "model-rejection-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.WithoutCancel(ctx)) })
	tools, err := interaction.NewToolSet(interaction.ToolSetConfig{Revision: "unexecuted", Tools: []tool.Tool{unexecutedTool{t: t}}})
	if err != nil {
		t.Fatal(err)
	}
	program, err := interaction.Compile(runtime.Compiler(), interaction.Config{
		Name:        "test.model_rejection",
		Description: "Reject an unusable model response.",
		Revision:    "fixture-model",
		Tools:       tools,
		Model: interaction.CallModel(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
			return response, nil
		})),
	})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := runtime.Start(ctx, program, run.StartRequest[interaction.Input]{
		ID:    run.StartID{Scope: "test", Key: "model-rejection"},
		Input: interaction.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("work"))}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := handle.Await(ctx)
	if err != nil {
		t.Fatal(err)
	}
	log, err := runtime.History(ctx, handle.ID())
	if err != nil {
		t.Fatal(err)
	}
	return result, log
}

func rootExit(t *testing.T, log run.Log) run.Exited {
	t.Helper()
	for _, entry := range log.Entries {
		if exited, ok := entry.Record.(run.Exited); ok && exited.Process == log.Root {
			return exited
		}
	}
	t.Fatal("log has no root Exited record")
	return run.Exited{}
}

func terminalFailure(t *testing.T, termination run.Termination) run.FailureClassification {
	t.Helper()
	failure, ok := termination.Failure()
	if !ok {
		t.Fatalf("termination %+v carries no Failure", termination)
	}
	return failure.Classification()
}

func TestToolCallsWithoutAToolRoundHaveAPublicClassification(t *testing.T) {
	response := &chat.Response{Output: &chat.Output{
		Message:      new(chat.NewAssistantMessage(chat.NewToolCallPart(chat.ToolCall{ID: "call", Name: "write", Arguments: `{}`}))),
		FinishReason: chat.FinishReasonStop,
	}}
	result, log := rejectedModelResponse(t, response)
	invalidResponse := interaction.ErrInvalidModelResponse.Classification()
	if interaction.FailureToolCallsNotCompleted == invalidResponse {
		t.Fatal("Tool-call rejection shares the missing-message classification")
	}
	for source, classification := range map[string]run.FailureClassification{
		"result":   terminalFailure(t, result.Termination()),
		"log exit": terminalFailure(t, rootExit(t, log).Termination),
	} {
		if classification != interaction.FailureToolCallsNotCompleted {
			t.Errorf("%s classification = %+v, want FailureToolCallsNotCompleted", source, classification)
		}
	}
	// The Step commits as a failed Transition that consumes the model's
	// Completion, so the Runtime records no rejected Step.
	var failed []run.Stepped
	for _, entry := range log.Entries {
		switch record := entry.Record.(type) {
		case run.Stepped:
			if record.Transition.Kind() == run.TransitionKindFail {
				failed = append(failed, record)
			}
		case run.ExecutionFailed:
			t.Fatalf("Tool-call rejection was recorded as a rejected Step: %+v", record)
		}
	}
	if len(failed) != 1 {
		t.Fatalf("failed Transitions = %d, want 1", len(failed))
	}
	stepFailure, _ := failed[0].Transition.Failure()
	if stepFailure.Classification() != interaction.FailureToolCallsNotCompleted || failed[0].Transition.Consumed() == 0 || failed[0].Transition.Consumed() != failed[0].Delivered {
		t.Fatalf("failed Transition = %+v, want the classified failure consuming every delivered message", failed[0])
	}
}

func TestMissingModelMessageKeepsItsOwnClassification(t *testing.T) {
	result, log := rejectedModelResponse(t, &chat.Response{Output: &chat.Output{FinishReason: chat.FinishReasonStop}})
	for source, classification := range map[string]run.FailureClassification{
		"result":   terminalFailure(t, result.Termination()),
		"log exit": terminalFailure(t, rootExit(t, log).Termination),
	} {
		if classification != interaction.ErrInvalidModelResponse.Classification() || classification == interaction.FailureToolCallsNotCompleted {
			t.Errorf("%s classification = %+v, want only ErrInvalidModelResponse", source, classification)
		}
	}
}
