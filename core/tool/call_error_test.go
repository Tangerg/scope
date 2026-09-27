package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestCallErrorPreservesEvidenceWithoutEstablishingFailure(t *testing.T) {
	cause := fmt.Errorf("lost acknowledgement: %w", context.DeadlineExceeded)
	evidence := chat.ToolOutput{
		Content: []chat.ToolContent{{Kind: chat.PartText, Text: "write acknowledged"}},
		Details: json.RawMessage(`{"written":["first"]}`),
	}
	want := evidence.Clone()
	callErr, err := tool.NewCallError(tool.CallErrorConfig{Cause: cause, Evidence: evidence})
	if err != nil {
		t.Fatal(err)
	}
	evidence.Content[0].Text = "changed"
	evidence.Details[0] = '!'
	wrapped := fmt.Errorf("tool call: %w", callErr)
	if !errors.Is(wrapped, cause) || !errors.Is(wrapped, context.DeadlineExceeded) {
		t.Fatalf("call error hid its original contract: %v", wrapped)
	}
	if failure, found := errors.AsType[*tool.Failure](wrapped); found {
		t.Fatalf("observed evidence became a definite failure: %v", failure)
	}
	got, found := errors.AsType[*tool.CallError](wrapped)
	if !found || !reflect.DeepEqual(got.Evidence(), want) {
		t.Fatalf("call evidence = %+v, error = %v", got, wrapped)
	}
	copy := got.Evidence()
	copy.Content[0].Text = "changed again"
	copy.Details[0] = '!'
	if !reflect.DeepEqual(got.Evidence(), want) {
		t.Fatal("call error evidence shares caller storage")
	}
}

func TestCallErrorRequiresValidCauseAndEvidence(t *testing.T) {
	for _, config := range []tool.CallErrorConfig{
		{},
		{Cause: (*os.PathError)(nil)},
		{Cause: errors.New("lost result"), Evidence: chat.ToolOutput{Details: json.RawMessage(`{`)}},
	} {
		callErr, err := tool.NewCallError(config)
		if callErr != nil || !errors.Is(err, tool.ErrInvalidCallError) {
			t.Fatalf("NewCallError = %v, %v", callErr, err)
		}
	}
	for _, invalid := range []*tool.CallError{nil, {}} {
		if invalid.Error() != tool.ErrInvalidCallError.Error() || !errors.Is(invalid.Validate(), tool.ErrInvalidCallError) {
			t.Fatalf("invalid CallError = %+v", invalid)
		}
		if invalid.Unwrap() != nil || !reflect.DeepEqual(invalid.Evidence(), chat.ToolOutput{}) {
			t.Fatalf("invalid CallError retained observations: %+v", invalid)
		}
	}
}

func TestFuncPreservesNonfinalExecutionEvidence(t *testing.T) {
	want := chat.NewTextToolOutput("first file changed; second response missing")
	callErr, err := tool.NewCallError(tool.CallErrorConfig{Cause: context.DeadlineExceeded, Evidence: want})
	if err != nil {
		t.Fatal(err)
	}
	executable, err := tool.NewFunc(tool.FuncConfig{Name: "write"}, func(context.Context, struct{}) (string, error) {
		return "not a completed result", callErr
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := tool.Bind(executable)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := binding.Contract().Prepare(chat.ToolCall{ID: "write-1", Name: "write", Arguments: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	output, err := binding.Call(t.Context(), invocation)
	if !reflect.DeepEqual(output, chat.ToolOutput{}) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Call = %+v, %v", output, err)
	}
	observed, found := errors.AsType[*tool.CallError](err)
	if !found || !reflect.DeepEqual(observed.Evidence(), want) {
		t.Fatalf("Func lost execution evidence: %v", err)
	}
}
