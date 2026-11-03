package interaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/tool"
)

func completedDispatch(result toolCallResult) toolDispatchResult {
	return toolDispatchResult{Completion: &result}
}

func TestSettleToolCallPolicy(t *testing.T) {
	structured, err := tool.NewJSONOutput(json.RawMessage(`{"value":42}`))
	if err != nil {
		t.Fatal(err)
	}
	success, resultErr := settleToolCall(tool.Result{Output: structured}, nil)
	if resultErr != nil || !reflect.DeepEqual(success, completedDispatch(succeededCallResult(structured))) {
		t.Fatalf("success = %#v, error = %v", success, resultErr)
	}
	empty, resultErr := settleToolCall(tool.Result{}, nil)
	if resultErr != nil || !reflect.DeepEqual(empty, completedDispatch(toolCallResult{Disposition: ResultSucceeded})) {
		t.Fatalf("empty success = %#v, error = %v", empty, resultErr)
	}
	invalidOutput := tool.Output{Details: json.RawMessage(`{`)}
	invalid, resultErr := settleToolCall(tool.Result{Output: invalidOutput}, nil)
	if !errors.Is(resultErr, tool.ErrInvalidOutput) || !reflect.DeepEqual(invalid, toolDispatchResult{}) {
		t.Fatalf("invalid output result = %#v, error = %v", invalid, resultErr)
	}

	cause := errors.New(strings.Repeat("x", 3_000))
	unknown, resultErr := settleToolCall(tool.Result{Output: tool.NewTextOutput("ignored")}, cause)
	if !errors.Is(resultErr, cause) || !reflect.DeepEqual(unknown, toolDispatchResult{}) {
		t.Fatalf("unknown outcome = %#v, error = %v", unknown, resultErr)
	}
	completeFailure, err := tool.NewFailure(tool.FailureConfig{Kind: tool.FailureKindFailed, Cause: errors.New("partial execution"), Output: structured})
	if err != nil {
		t.Fatal(err)
	}
	complete, resultErr := settleToolCall(tool.Result{}, fmt.Errorf("wrapped: %w", completeFailure))
	if resultErr != nil || !reflect.DeepEqual(complete, completedDispatch(failedCallResult(structured))) {
		t.Fatalf("complete failure = %#v, error = %v", complete, resultErr)
	}

	for _, cause := range []error{errors.New("projection unavailable"), context.Canceled, context.DeadlineExceeded} {
		if result, resultErr := settleToolCall(tool.Result{Output: invalidOutput}, cause); !errors.Is(resultErr, cause) || !reflect.DeepEqual(result, toolDispatchResult{}) {
			t.Fatalf("control cause %v produced %#v, error = %v", cause, result, resultErr)
		}
	}
	authorizationCause, err := tool.NewFailure(tool.FailureConfig{
		Kind: tool.FailureKindRejected, Cause: errors.New("private authorization detail"),
		Output: tool.NewTextOutput("the requested operation is not permitted"),
	})
	if err != nil {
		t.Fatal(err)
	}
	denied, err := settleToolCall(tool.Result{Output: structured}, fmt.Errorf("wrapped: %w", authorizationCause))
	wantDenied := toolCallResult{Disposition: ResultRejected, Output: tool.NewTextOutput("the requested operation is not permitted")}
	if err != nil || !reflect.DeepEqual(denied, completedDispatch(wantDenied)) {
		t.Fatalf("authorization result=%+v error=%v", denied, err)
	}
	structured.Details[0] = '['
	if string(success.Completion.Output.Details) != `{"value":42}` || string(complete.Completion.Output.Details) != `{"value":42}` {
		t.Fatal("model feedback retained caller-owned output")
	}
}

func TestSettleToolCallPreservesDefiniteFailureCauses(t *testing.T) {
	output := tool.NewTextOutput("first write completed; second write did not start")
	want := completedDispatch(failedCallResult(output))
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		failure, err := tool.NewFailure(tool.FailureConfig{Kind: tool.FailureKindFailed, Cause: cause, Output: output})
		if err != nil {
			t.Fatal(err)
		}
		for _, wrapped := range []error{
			fmt.Errorf("wrapped: %w", failure),
			fmt.Errorf("host diagnostic: %w", failure),
			errors.Join(nil, failure),
		} {
			result, err := settleToolCall(tool.Result{Output: output}, wrapped)
			if err != nil || !reflect.DeepEqual(result, want) {
				t.Fatalf("diagnostic overrode definite failure: result=%+v error=%v", result, err)
			}
		}
	}
}

func TestSettleToolCallKeepsDiagnosticCausesOutOfControlFlow(t *testing.T) {
	output := tool.NewTextOutput("current call refused")
	want := completedDispatch(toolCallResult{Disposition: ResultRejected, Output: output})
	for _, cause := range []error{
		context.Canceled, context.DeadlineExceeded, errors.New("nested host failure"),
	} {
		failure, err := tool.NewFailure(tool.FailureConfig{Kind: tool.FailureKindRejected, Output: output, Cause: cause})
		if err != nil {
			t.Fatal(err)
		}
		result, err := settleToolCall(tool.Result{}, fmt.Errorf("current call: %w", failure))
		if err != nil || !reflect.DeepEqual(result, want) {
			t.Fatalf("cause=%v result=%+v error=%v", cause, result, err)
		}
	}
}

func TestSettleToolCallRejectsInvalidFailureValues(t *testing.T) {
	for _, failure := range []*tool.Failure{nil, {}} {
		cause := fmt.Errorf("invalid outcome: %w", failure)
		result, err := settleToolCall(tool.Result{}, cause)
		if !errors.Is(err, cause) || !reflect.DeepEqual(result, toolDispatchResult{}) {
			t.Fatalf("result=%+v error=%v", result, err)
		}
	}
}

func TestSettleToolCallDoesNotPromoteJoinedFailure(t *testing.T) {
	failure, err := tool.NewFailure(tool.FailureConfig{
		Kind: tool.FailureKindRejected, Output: tool.NewTextOutput("one operation refused"),
	})
	if err != nil {
		t.Fatal(err)
	}
	unknown := errors.New("another operation lost its result")
	cause := errors.Join(failure, unknown)
	result, err := settleToolCall(tool.Result{}, cause)
	if !errors.Is(err, failure) || !errors.Is(err, unknown) || !reflect.DeepEqual(result, toolDispatchResult{}) {
		t.Fatalf("independent outcomes became one definite result: result=%+v error=%v", result, err)
	}
}
