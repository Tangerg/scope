package tool_test

import (
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
	failure, err := tool.NewFailure(cause, output)
	if err != nil {
		t.Fatal(err)
	}
	output.Content[0].Text = "changed"
	output.Details[0] = '!'
	wrapped := fmt.Errorf("execution: %w", failure)
	if !errors.Is(wrapped, cause) {
		t.Fatal("failure lost its original cause")
	}
	got, found := errors.AsType[*tool.Failure](wrapped)
	if !found || !reflect.DeepEqual(got.Output(), want) {
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
		failure, err := tool.NewFailure(test.cause, test.output)
		if failure != nil || !errors.Is(err, tool.ErrInvalidFailure) {
			t.Fatalf("NewFailure = %v, %v", failure, err)
		}
	}
}

func TestNilFailureHasInvalidZeroBehavior(t *testing.T) {
	var failure *tool.Failure
	if failure.Error() != tool.ErrInvalidFailure.Error() || failure.Unwrap() != nil ||
		!reflect.DeepEqual(failure.Output(), chat.ToolOutput{}) {
		t.Fatalf("nil Failure = error %q, cause %v, output %+v", failure.Error(), failure.Unwrap(), failure.Output())
	}
}
