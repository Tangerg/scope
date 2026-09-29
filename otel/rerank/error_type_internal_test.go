package rerank

import (
	"context"
	"errors"
	"fmt"
	"testing"

	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"

	corererank "github.com/Tangerg/scope/core/rerank"
)

func TestErrorTypeAttributeStaysLowCardinality(t *testing.T) {
	cases := map[string]struct {
		err  error
		want string
	}{
		"canceled":          {err: fmt.Errorf("provider: %w", context.Canceled), want: "context.canceled"},
		"deadline exceeded": {err: fmt.Errorf("provider: %w", context.DeadlineExceeded), want: "context.deadline_exceeded"},
		"invalid request":   {err: fmt.Errorf("provider: %w", corererank.ErrInvalidRequest), want: errorInvalidRequest},
		"invalid options":   {err: fmt.Errorf("provider: %w", corererank.ErrInvalidOptions), want: errorInvalidRequest},
		"invalid response":  {err: fmt.Errorf("provider: %w", corererank.ErrInvalidResponse), want: errorInvalidOutput},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			attribute := errorTypeAttribute(testCase.err)
			if attribute.Key != semconv.ErrorTypeKey || attribute.Value.AsString() != testCase.want {
				t.Fatalf("error type = %v, want %q", attribute, testCase.want)
			}
		})
	}
}

func TestUnclassifiedErrorsFallBackToTheirType(t *testing.T) {
	message := "a very specific provider message with an id 12345"
	if got := errorTypeAttribute(errors.New(message)).Value.AsString(); got == message {
		t.Fatal("the error message became the metric dimension")
	}
}
