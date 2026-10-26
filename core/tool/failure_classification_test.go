package tool_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestClassifyFailureRespectsInvocationBoundaries(t *testing.T) {
	unknown := errors.New("outcome unknown")
	definite, err := tool.NewFailure(tool.FailureConfig{
		Kind: tool.FailureKindFailed, Cause: unknown, Output: chat.NewTextToolOutput("operation failed"),
	})
	if err != nil {
		t.Fatal(err)
	}
	outer, err := tool.NewFailure(tool.FailureConfig{
		Kind: tool.FailureKindFailed, Cause: errors.Join(definite, unknown), Output: chat.NewTextToolOutput("complete outer outcome"),
	})
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := tool.NewCallError(tool.CallErrorConfig{Cause: definite, Evidence: chat.NewTextToolOutput("observed work")})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		err  error
		want *tool.Failure
	}{
		{name: "nil"},
		{name: "ordinary", err: unknown},
		{name: "typed nil", err: (*tool.Failure)(nil)},
		{name: "invalid", err: new(tool.Failure)},
		{name: "definite", err: definite, want: definite},
		{name: "transparent", err: fmt.Errorf("operation: %w", definite), want: definite},
		{name: "single join", err: errors.Join(nil, definite), want: definite},
		{name: "evidence", err: evidence, want: definite},
		{name: "unknown first", err: errors.Join(unknown, definite)},
		{name: "unknown last", err: errors.Join(definite, unknown)},
		{name: "two outcomes", err: errors.Join(definite, outer)},
		{name: "nested ambiguity", err: fmt.Errorf("operation: %w", errors.Join(definite, unknown))},
		{name: "outer owner", err: outer, want: outer},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := tool.ClassifyFailure(test.err)
			if got != test.want || ok != (test.want != nil) {
				t.Fatalf("ClassifyFailure(%v) = %v, %t; want %v", test.err, got, ok, test.want)
			}
		})
	}
}
