package errortelemetry

import (
	"context"
	"errors"
	"fmt"
	"testing"

	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
)

type classifiedError struct{}

func (classifiedError) Error() string { return "provider message with id 12345" }

func TestClassifyPrefersContextThenDeclaredClasses(t *testing.T) {
	invalid := errors.New("invalid request")
	other := errors.New("invalid response")
	classes := []Class{{Err: invalid, Type: "capability.invalid_request"}, {Err: other, Type: "capability.invalid_response"}}
	cases := map[string]struct {
		err  error
		want string
	}{
		"canceled":            {err: fmt.Errorf("provider: %w", context.Canceled), want: "context.canceled"},
		"deadline":            {err: fmt.Errorf("provider: %w", context.DeadlineExceeded), want: "context.deadline_exceeded"},
		"canceled over class": {err: errors.Join(invalid, context.Canceled), want: "context.canceled"},
		"first class":         {err: fmt.Errorf("provider: %w", invalid), want: "capability.invalid_request"},
		"second class":        {err: fmt.Errorf("provider: %w", other), want: "capability.invalid_response"},
		"unclassified":        {err: classifiedError{}, want: "github.com/Tangerg/scope/otel/internal/errortelemetry.classifiedError"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			got := Classify(testCase.err, classes...)
			if got.Key != semconv.ErrorTypeKey || got.Value.AsString() != testCase.want {
				t.Fatalf("Classify() = %s=%q, want %s=%q", got.Key, got.Value.AsString(), semconv.ErrorTypeKey, testCase.want)
			}
		})
	}
}
