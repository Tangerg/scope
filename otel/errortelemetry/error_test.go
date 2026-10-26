package errortelemetry

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"
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
		"unclassified":        {err: classifiedError{}, want: "github.com/Tangerg/scope/otel/errortelemetry.classifiedError"},
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

func TestFinishPreservesReturnedErrorsAndPanics(t *testing.T) {
	cause := errors.New("private-provider-message")
	for _, returned := range []error{nil, cause} {
		var observed error
		calls := 0
		call := func() (err error) {
			defer Finish(&err, func(value error) { observed = value; calls++ })
			return returned
		}
		if got := call(); got != returned || observed != returned || calls != 1 { //nolint:errorlint // The delegate owns the exact returned error.
			t.Fatalf("returned=%v observed=%v calls=%d, want original outcome once", got, observed, calls)
		}
	}
	var observed error
	var propagated any
	func() {
		defer func() { propagated = recover() }()
		var err error
		defer Finish(&err, func(value error) { observed = value })
		panic(cause)
	}()
	if propagated != cause || observed == nil || observed.Error() != "panic" || Classify(observed).Value.AsString() != "panic" { //nolint:errorlint // The original panic value must propagate unchanged.
		t.Fatalf("panic=%v observed=%v, want original panic and content-free classification", propagated, observed)
	}
}

type exceptionLogger struct {
	log.Logger
	ctx     context.Context
	records []log.Record
}

func (e *exceptionLogger) Emit(ctx context.Context, record log.Record) {
	e.ctx = ctx
	e.records = append(e.records, record.Clone())
}

func TestEmitGenAIExceptionRetainsCorrelationAfterCancellation(t *testing.T) {
	spanContext := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled,
	})
	ctx, cancel := context.WithCancel(trace.ContextWithSpanContext(t.Context(), spanContext))
	cancel()
	logger := &exceptionLogger{}
	occurredAt := time.Now()
	EmitGenAIException(ctx, logger, Classify(context.Canceled), occurredAt)
	if len(logger.records) != 1 || logger.ctx.Err() != nil || !trace.SpanContextFromContext(logger.ctx).Equal(spanContext) {
		t.Fatalf("exception lost cancellation correlation: records=%d context=%v", len(logger.records), logger.ctx)
	}
	record := logger.records[0]
	if record.EventName() != "gen_ai.client.operation.exception" || !record.Timestamp().Equal(occurredAt) || record.Severity() != log.SeverityWarn || record.SeverityText() != "WARN" {
		t.Fatalf("exception event identity changed: %+v", record)
	}
	if record.Body().Type() != attribute.EMPTY || record.AttributesLen() != 1 {
		t.Fatalf("exception contains unexpected content: body=%v attributes=%d", record.Body(), record.AttributesLen())
	}
	record.WalkAttributes(func(value attribute.KeyValue) bool {
		if value.Key != semconv.ExceptionTypeKey || value.Value.AsString() != "context.canceled" {
			t.Errorf("exception attribute = %v", value)
		}
		return true
	})
}

func TestRecordKeepsOnlyStableClassification(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{name: "cancellation", err: context.Canceled, want: "context.canceled"},
		{name: "deadline", err: context.DeadlineExceeded, want: "context.deadline_exceeded"},
		{name: "ordinary error", err: errors.New("private-message"), want: "*errors.errorString"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(recorder))
			t.Cleanup(func() {
				if err := provider.Shutdown(context.Background()); err != nil {
					t.Error(err)
				}
			})
			_, span := provider.Tracer("test").Start(t.Context(), "failure")
			occurredAt := time.Now()
			Record(span, Classify(fmt.Errorf("private-wrapper: %w", test.err)), trace.WithTimestamp(occurredAt))
			span.End()
			spans := recorder.Ended()
			if len(spans) != 1 {
				t.Fatalf("spans = %d, want 1", len(spans))
			}
			recorded := spans[0]
			if recorded.Status().Code != codes.Error || recorded.Status().Description != test.want {
				t.Errorf("status = %v, want Error/%s", recorded.Status(), test.want)
			}
			attributes := recorded.Attributes()
			if len(attributes) != 1 || attributes[0].Key != semconv.ErrorTypeKey || attributes[0].Value.AsString() != test.want {
				t.Errorf("attributes = %v, want only error.type=%s", attributes, test.want)
			}
			events := recorded.Events()
			if len(events) != 1 || events[0].Name != "exception" || !events[0].Time.Equal(occurredAt) || len(events[0].Attributes) != 1 {
				t.Fatalf("events = %v, want one exception at the observed time", events)
			}
			attribute := events[0].Attributes[0]
			if attribute.Key != semconv.ExceptionTypeKey || attribute.Value.AsString() != test.want {
				t.Errorf("exception attribute = %v, want exception.type=%s", attribute, test.want)
			}
			if strings.Contains(fmt.Sprint(recorded.Attributes(), recorded.Events(), recorded.Status()), "private") {
				t.Fatal("telemetry contains raw error content")
			}
		})
	}
}
