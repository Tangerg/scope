package interaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestModelToolResultPolicy(t *testing.T) {
	call := chat.ToolCall{ID: "call", Name: "feedback", Arguments: `{}`}
	structured, err := chat.NewJSONToolOutput(json.RawMessage(`{"value":42}`))
	if err != nil {
		t.Fatal(err)
	}
	success, _, _, resultErr := modelToolResult(call, structured, nil)
	wantSuccess := chat.ToolResult{ID: call.ID, Name: call.Name, Output: structured}
	if resultErr != nil || !reflect.DeepEqual(success, wantSuccess) {
		t.Fatalf("success = %#v, error = %v", success, resultErr)
	}
	empty, _, _, resultErr := modelToolResult(call, chat.ToolOutput{}, nil)
	if resultErr != nil || !reflect.DeepEqual(empty, chat.ToolResult{ID: call.ID, Name: call.Name}) {
		t.Fatalf("empty success = %#v, error = %v", empty, resultErr)
	}
	invalidOutput := chat.ToolOutput{Details: json.RawMessage(`{`)}
	invalid, _, _, resultErr := modelToolResult(call, invalidOutput, nil)
	if !errors.Is(resultErr, chat.ErrInvalidToolOutput) || !reflect.DeepEqual(invalid, chat.ToolResult{}) {
		t.Fatalf("invalid output result = %#v, error = %v", invalid, resultErr)
	}

	cause := errors.New(strings.Repeat("x", 3_000))
	failure, _, _, resultErr := modelToolResult(call, chat.NewTextToolOutput("ignored"), cause)
	if !errors.Is(resultErr, cause) || !reflect.DeepEqual(failure, chat.ToolResult{}) {
		t.Fatalf("unknown outcome = %#v, error = %v", failure, resultErr)
	}
	completeFailure, err := tool.NewFailure(tool.FailureConfig{Kind: tool.FailureKindFailed, Cause: errors.New("partial execution"), Output: structured})
	if err != nil {
		t.Fatal(err)
	}
	complete, _, _, resultErr := modelToolResult(call, chat.ToolOutput{}, fmt.Errorf("wrapped: %w", completeFailure))
	if resultErr != nil || !reflect.DeepEqual(complete, chat.ToolResult{ID: call.ID, Name: call.Name, IsError: true, Output: structured}) {
		t.Fatalf("complete failure = %#v, error = %v", complete, resultErr)
	}

	controlCauses := []error{
		HostFailure(errors.New("projection unavailable")),
		context.Canceled,
		context.DeadlineExceeded,
	}
	for _, cause := range controlCauses {
		if result, _, _, resultErr := modelToolResult(call, invalidOutput, cause); !errors.Is(resultErr, cause) || !reflect.DeepEqual(result, chat.ToolResult{}) {
			t.Fatalf("control cause %v produced %#v, error = %v", cause, result, resultErr)
		}
	}
	if result, _, _, resultErr := modelToolResult(chat.ToolCall{}, chat.NewTextToolOutput("ignored"), nil); resultErr == nil || !reflect.DeepEqual(result, chat.ToolResult{}) {
		t.Fatalf("invalid call produced %#v, error = %v", result, resultErr)
	}
	authorizationCause, err := tool.NewFailure(tool.FailureConfig{
		Kind: tool.FailureKindRejected, Cause: errors.New("private authorization detail"),
		Output: chat.NewTextToolOutput("the requested operation is not permitted"),
	})
	if err != nil {
		t.Fatal(err)
	}
	denied, _, rejected, err := modelToolResult(call, structured, fmt.Errorf("wrapped: %w", authorizationCause))
	wantDenied := chat.ToolResult{ID: call.ID, Name: call.Name, IsError: true,
		Output: chat.NewTextToolOutput("the requested operation is not permitted")}
	if err != nil || !rejected || !reflect.DeepEqual(denied, wantDenied) {
		t.Fatalf("authorization result=%+v error=%v", denied, err)
	}
	structured.Details[0] = '['
	if string(success.Output.Details) != `{"value":42}` || string(complete.Output.Details) != `{"value":42}` {
		t.Fatal("model feedback retained caller-owned output")
	}
}

func TestModelToolResultPreservesDefiniteFailureCauses(t *testing.T) {
	call := chat.ToolCall{ID: "call", Name: "feedback", Arguments: `{}`}
	output := chat.NewTextToolOutput("first write completed; second write did not start")
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		failure, err := tool.NewFailure(tool.FailureConfig{Kind: tool.FailureKindFailed, Cause: cause, Output: output})
		if err != nil {
			t.Fatal(err)
		}
		result, required, _, err := modelToolResult(call, chat.ToolOutput{}, fmt.Errorf("wrapped: %w", failure))
		if err != nil || required != nil || !reflect.DeepEqual(result, chat.ToolResult{ID: call.ID, Name: call.Name, IsError: true, Output: output}) {
			t.Fatalf("cause %v: result=%+v required=%v error=%v", cause, result, required, err)
		}
		for _, conflicting := range []error{HostFailure(failure), errors.Join(failure, ErrHostFailure)} {
			result, required, _, err := modelToolResult(call, output, conflicting)
			if !errors.Is(err, ErrHostFailure) || required != nil || !reflect.DeepEqual(result, chat.ToolResult{}) {
				t.Fatalf("host failure: result=%+v required=%v error=%v", result, required, err)
			}
		}
	}
}

func TestModelToolResultKeepsDiagnosticCausesOutOfControlFlow(t *testing.T) {
	call := chat.ToolCall{ID: "call", Name: "feedback", Arguments: `{}`}
	output := chat.NewTextToolOutput("current call refused")
	for _, cause := range []error{
		context.Canceled, context.DeadlineExceeded, HostFailure(errors.New("nested host failure")),
		RequireToolInput(json.RawMessage(`"inner prompt"`), json.RawMessage(`{"type":"boolean"}`), json.RawMessage(`{}`)),
	} {
		failure, err := tool.NewFailure(tool.FailureConfig{Kind: tool.FailureKindRejected, Output: output, Cause: cause})
		if err != nil {
			t.Fatal(err)
		}
		result, required, rejected, err := modelToolResult(call, chat.ToolOutput{}, fmt.Errorf("current call: %w", failure))
		if err != nil || required != nil || !rejected || !reflect.DeepEqual(result, chat.ToolResult{ID: call.ID, Name: call.Name, Output: output, IsError: true}) {
			t.Fatalf("cause=%v result=%+v required=%v rejected=%v error=%v", cause, result, required, rejected, err)
		}
	}
}

func TestModelToolResultRejectsInvalidFailureValues(t *testing.T) {
	for _, failure := range []*tool.Failure{nil, {}} {
		result, required, rejected, err := modelToolResult(chat.ToolCall{ID: "call", Name: "feedback", Arguments: `{}`}, chat.ToolOutput{}, fmt.Errorf("invalid outcome: %w", failure))
		if !errors.Is(err, tool.ErrInvalidFailure) || required != nil || rejected || !reflect.DeepEqual(result, chat.ToolResult{}) {
			t.Fatalf("result=%+v required=%v rejected=%v error=%v", result, required, rejected, err)
		}
	}
}
