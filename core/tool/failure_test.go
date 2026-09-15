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

func TestFailurePreservesCauseAndOwnedOutput(t *testing.T) {
	cause := errors.New("operation failed")
	output := chat.ToolOutput{
		Content: []chat.ToolContent{{Kind: chat.PartText, Text: "first"}, {Kind: chat.PartText, Text: "second"}},
		Details: json.RawMessage(`{"committed":["one"]}`),
	}
	want := output.Clone()
	failure, err := tool.NewFailure(tool.FailureConfig{Kind: tool.FailureKindFailed, Cause: cause, Output: output})
	if err != nil {
		t.Fatal(err)
	}
	output.Content[0].Text = "changed"
	output.Details[0] = '!'
	wrapped := fmt.Errorf("execution: %w", failure)
	if errors.Is(wrapped, cause) || !reflect.ValueOf(failure.Cause()).Equal(reflect.ValueOf(cause)) {
		t.Fatal("diagnostic cause crossed the outcome boundary or was lost")
	}
	got, found := errors.AsType[*tool.Failure](wrapped)
	if !found || got.Kind() != tool.FailureKindFailed || !reflect.DeepEqual(got.Output(), want) {
		t.Fatalf("wrapped failure = %v", wrapped)
	}
	copy := got.Output()
	copy.Content[0].Text = "changed again"
	copy.Details[0] = '!'
	if !reflect.DeepEqual(got.Output(), want) {
		t.Fatal("failure output shares caller-owned storage")
	}
}

func TestFailureRejectsIncompleteConstruction(t *testing.T) {
	for _, test := range []struct {
		cause  error
		output chat.ToolOutput
	}{
		{},
		{cause: (*os.PathError)(nil)},
		{cause: errors.New("failed"), output: chat.ToolOutput{Details: json.RawMessage(`{`)}},
	} {
		failure, err := tool.NewFailure(tool.FailureConfig{Kind: tool.FailureKindFailed, Cause: test.cause, Output: test.output})
		if failure != nil || !errors.Is(err, tool.ErrInvalidFailure) {
			t.Fatalf("NewFailure = %v, %v", failure, err)
		}
	}
}

func TestNilFailureHasInvalidZeroBehavior(t *testing.T) {
	var failure *tool.Failure
	if failure.Error() != tool.ErrInvalidFailure.Error() || failure.Cause() != nil ||
		!reflect.DeepEqual(failure.Output(), chat.ToolOutput{}) {
		t.Fatalf("nil Failure = error %q, cause %v, output %+v", failure.Error(), failure.Cause(), failure.Output())
	}
}

func TestFailureKindAndDiagnosticsHaveSeparateAuthority(t *testing.T) {
	inner, err := tool.NewFailure(tool.FailureConfig{
		Kind: tool.FailureKindRejected, Output: chat.NewTextToolOutput("inner refusal"),
	})
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.Join(context.DeadlineExceeded, inner)
	outer, err := tool.NewFailure(tool.FailureConfig{
		Kind: tool.FailureKindFailed, Cause: cause, Output: chat.NewTextToolOutput("complete current outcome"),
	})
	if err != nil {
		t.Fatal(err)
	}
	wrapped := fmt.Errorf("outer call: %w", outer)
	got, found := errors.AsType[*tool.Failure](wrapped)
	if !found || got != outer || got.Kind() != tool.FailureKindFailed ||
		!reflect.ValueOf(got.Cause()).Equal(reflect.ValueOf(cause)) || errors.Is(wrapped, context.DeadlineExceeded) || errors.Is(wrapped, inner) {
		t.Fatalf("outcome=%+v error=%v", got, wrapped)
	}
	if inner.Cause() != nil || inner.Error() != "tool: rejected" {
		t.Fatalf("error-free policy refusal = %v", inner)
	}
	for _, config := range []tool.FailureConfig{
		{}, {Kind: "unknown"}, {Kind: tool.FailureKindRejected, Cause: (*os.PathError)(nil)},
	} {
		if value, err := tool.NewFailure(config); value != nil || !errors.Is(err, tool.ErrInvalidFailure) {
			t.Fatalf("invalid failure = %v, %v", value, err)
		}
	}
	for _, invalid := range []*tool.Failure{nil, {}} {
		if !errors.Is(invalid.Validate(), tool.ErrInvalidFailure) || invalid.Kind() != "" {
			t.Fatalf("invalid zero Failure = %+v", invalid)
		}
	}
}
