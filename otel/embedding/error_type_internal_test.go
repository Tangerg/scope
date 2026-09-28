package embedding

import (
	"context"
	"errors"
	"fmt"
	"testing"

	coreembedding "github.com/Tangerg/scope/core/embedding"

	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

func TestErrorTypeAttributeStaysLowCardinality(t *testing.T) {
	cases := map[string]struct {
		err  error
		want string
	}{
		"canceled": {
			err:  fmt.Errorf("provider: %w", context.Canceled),
			want: errorCanceled,
		},
		"deadline exceeded": {
			err:  fmt.Errorf("provider: %w", context.DeadlineExceeded),
			want: errorDeadline,
		},
		"invalid request": {
			err:  fmt.Errorf("provider: %w", coreembedding.ErrInvalidRequest),
			want: errorInvalidRequest,
		},
		"invalid options": {
			err:  fmt.Errorf("provider: %w", coreembedding.ErrInvalidOptions),
			want: errorInvalidRequest,
		},
		"invalid response": {
			err:  fmt.Errorf("provider: %w", coreembedding.ErrInvalidResponse),
			want: errorInvalidOutput,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			attribute := errorTypeAttribute(testCase.err)
			if attribute.Key != semconv.ErrorTypeKey {
				t.Fatalf("attribute key = %q, want %q", attribute.Key, semconv.ErrorTypeKey)
			}
			if got := attribute.Value.AsString(); got != testCase.want {
				t.Fatalf("error type = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestUnclassifiedErrorsFallBackToTheirType(t *testing.T) {
	attribute := errorTypeAttribute(errors.New("a very specific provider message with an id 12345"))
	if got := attribute.Value.AsString(); got == "a very specific provider message with an id 12345" {
		t.Fatal("the error message became the metric dimension")
	}
}
