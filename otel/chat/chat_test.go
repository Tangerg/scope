package chat_test

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/Tangerg/scope/core/chat"
	otelchat "github.com/Tangerg/scope/otel/chat"
)

type telemetryRig struct {
	spans  *tracetest.SpanRecorder
	reader *sdkmetric.ManualReader
	traces *sdktrace.TracerProvider
	meters *sdkmetric.MeterProvider
}

func newRig(t *testing.T, provider string) (otelchat.Middleware, *telemetryRig) {
	t.Helper()
	spans := tracetest.NewSpanRecorder()
	traces := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	reader := sdkmetric.NewManualReader()
	meters := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		_ = traces.Shutdown(context.Background())
		_ = meters.Shutdown(context.Background())
	})
	middleware, err := otelchat.NewMiddleware(otelchat.MiddlewareConfig{
		Provider:       provider,
		TracerProvider: traces,
		MeterProvider:  meters,
	})
	if err != nil {
		t.Fatal(err)
	}
	return middleware, &telemetryRig{spans: spans, reader: reader, traces: traces, meters: meters}
}

func request(model string) *chat.Request {
	maxTokens := int64(100)
	temperature := 0.25
	topP := 0.9
	topK := int64(40)
	frequency := 0.1
	presence := -0.1
	return &chat.Request{
		Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))},
		Options: chat.Options{
			Model:            model,
			MaxOutputTokens:  &maxTokens,
			Temperature:      &temperature,
			TopP:             &topP,
			TopK:             &topK,
			FrequencyPenalty: &frequency,
			PresencePenalty:  &presence,
			Stop:             []string{"END"},
		},
	}
}

func response(text string, finish chat.FinishReason, input, output int64) *chat.Response {
	message := chat.NewAssistantMessage(chat.NewTextPart(text))
	return &chat.Response{
		Output: &chat.Output{Message: &message, FinishReason: finish},
		Metadata: &chat.ResponseMetadata{
			ID: "response-1", Model: "served-model",
			Usage: chat.Usage{InputTokens: input, OutputTokens: output},
		},
	}
}

func responseDelta(text string, finish chat.FinishReason, input, output int64) *chat.ResponseDelta {
	delta := &chat.ResponseDelta{
		FinishReason: finish,
		Metadata: &chat.ResponseMetadata{
			ID: "response-1", Model: "served-model",
			Usage: chat.Usage{InputTokens: input, OutputTokens: output},
		},
	}
	if text != "" {
		delta.Parts = []chat.PartDelta{chat.NewTextDelta(text)}
	}
	return delta
}

func TestNewMiddlewareValidatesAndNormalizesProvider(t *testing.T) {
	if _, err := otelchat.NewMiddleware(otelchat.MiddlewareConfig{}); !errors.Is(err, otelchat.ErrInvalidConfig) {
		t.Fatalf("NewMiddleware error = %v, want ErrInvalidConfig", err)
	}

	middleware, rig := newRig(t, "  OpenAI  ")
	wrapped := middleware.Call(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return &chat.Response{}, nil
	}))
	if _, err := wrapped.Call(t.Context(), request("model")); err != nil {
		t.Fatal(err)
	}
	attrs := spanAttributes(t, rig.spans.Ended()[0])
	if got := attrs["gen_ai.provider.name"].AsString(); got != "openai" {
		t.Fatalf("gen_ai.provider.name = %q, want openai", got)
	}

	var traces *sdktrace.TracerProvider
	var meters *sdkmetric.MeterProvider
	if _, err := otelchat.NewMiddleware(otelchat.MiddlewareConfig{
		Provider: "openai", TracerProvider: traces, MeterProvider: meters,
	}); err != nil {
		t.Fatalf("typed nil providers must use global defaults: %v", err)
	}
}

func TestMiddlewarePreservesMissingCapabilities(t *testing.T) {
	middleware, _ := newRig(t, "openai")
	var model chat.ModelFunc
	var streamer chat.StreamerFunc
	if middleware.Call(model) != nil {
		t.Fatal("Call synthesized a model capability")
	}
	if middleware.Stream(streamer) != nil {
		t.Fatal("Stream synthesized a streaming capability")
	}
}

func TestZeroMiddlewareReportsInvalidConfigWithoutPanicking(t *testing.T) {
	var middleware otelchat.Middleware
	model := middleware.Call(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return &chat.Response{}, nil
	}))
	if _, err := model.Call(t.Context(), request("model")); !errors.Is(err, otelchat.ErrInvalidConfig) {
		t.Fatalf("Call error = %v, want ErrInvalidConfig", err)
	}

	streamer := middleware.Stream(chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
		return func(func(*chat.ResponseDelta, error) bool) {}
	}))
	var got error
	for _, err := range streamer.Stream(t.Context(), request("model")) {
		got = err
	}
	if !errors.Is(got, otelchat.ErrInvalidConfig) {
		t.Fatalf("Stream error = %v, want ErrInvalidConfig", got)
	}
}

func TestCallRecordsCurrentGenAISemantics(t *testing.T) {
	middleware, rig := newRig(t, "anthropic")
	want := &chat.Response{
		Output: &chat.Output{FinishReason: chat.FinishReasonStop},
		Metadata: &chat.ResponseMetadata{
			ID: "response-1", Model: "claude-served",
			Usage: chat.Usage{InputTokens: 12, OutputTokens: 7},
		},
	}
	var sawSpanContext bool
	model := chat.ModelFunc(func(ctx context.Context, got *chat.Request) (*chat.Response, error) {
		sawSpanContext = trace.SpanFromContext(ctx).SpanContext().IsValid() && got.Options.Model == "claude-requested"
		return want, nil
	})

	got, err := middleware.Call(model).Call(t.Context(), request("claude-requested"))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatal("middleware replaced the provider response")
	}
	if !sawSpanContext {
		t.Fatal("wrapped model did not receive the original request")
	}

	ended := rig.spans.Ended()
	if len(ended) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(ended))
	}
	span := ended[0]
	if span.Name() != "chat claude-requested" || span.SpanKind() != trace.SpanKindClient {
		t.Fatalf("span name/kind = %q/%v", span.Name(), span.SpanKind())
	}
	attrs := spanAttributes(t, span)
	assertStringAttr(t, attrs, "gen_ai.provider.name", "anthropic")
	assertStringAttr(t, attrs, "gen_ai.operation.name", "chat")
	assertStringAttr(t, attrs, "gen_ai.request.model", "claude-requested")
	assertStringAttr(t, attrs, "gen_ai.response.model", "claude-served")
	assertStringAttr(t, attrs, "gen_ai.response.id", "response-1")
	if got := attrs["gen_ai.response.finish_reasons"].AsStringSlice(); len(got) != 1 || got[0] != "stop" {
		t.Fatalf("finish reasons = %v", got)
	}
	if got := attrs["gen_ai.usage.input_tokens"].AsInt64(); got != 12 {
		t.Fatalf("input tokens = %d", got)
	}
	if got := attrs["gen_ai.usage.output_tokens"].AsInt64(); got != 7 {
		t.Fatalf("output tokens = %d", got)
	}
	if _, legacy := attrs["gen_ai.system"]; legacy {
		t.Fatal("legacy gen_ai.system attribute must not be emitted")
	}

	metrics := collectMetrics(t, rig.reader)
	if got := histogramInt64Sum(t, metrics, "gen_ai.client.token.usage", "gen_ai.token.type", "input"); got != 12 {
		t.Fatalf("input token metric = %d, want 12", got)
	}
	if got := histogramInt64Sum(t, metrics, "gen_ai.client.token.usage", "gen_ai.token.type", "output"); got != 7 {
		t.Fatalf("output token metric = %d, want 7", got)
	}
	assertMetricAttribute(t, metrics, "gen_ai.client.token.usage", "gen_ai.provider.name", "anthropic")
	assertMetricAttribute(t, metrics, "gen_ai.client.operation.duration", "gen_ai.request.model", "claude-requested")
}

func TestCallPreservesResponseAndError(t *testing.T) {
	middleware, rig := newRig(t, "openai")
	wantResponse := response("partial", "", 4, 2)
	wantErr := errors.New("provider failed")
	gotResponse, gotErr := middleware.Call(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return wantResponse, wantErr
	})).Call(t.Context(), request("gpt"))
	if gotResponse != wantResponse || !errors.Is(gotErr, wantErr) {
		t.Fatalf("response/error = %p/%v, want %p/%v", gotResponse, gotErr, wantResponse, wantErr)
	}
	span := rig.spans.Ended()[0]
	if span.Status().Code != codes.Error || len(span.Events()) != 1 || span.Events()[0].Name != "exception" {
		t.Fatalf("error span status/events = %v/%v", span.Status(), span.Events())
	}
	assertStringAttr(t, spanAttributes(t, span), "gen_ai.response.model", "served-model")
	metrics := collectMetrics(t, rig.reader)
	if got := histogramInt64Sum(t, metrics, "gen_ai.client.token.usage", "gen_ai.token.type", "input"); got != 4 {
		t.Fatalf("known input token usage = %d, want 4", got)
	}
	assertMetricAttribute(t, metrics, "gen_ai.client.operation.duration", "error.type", "*errors.errorString")
}

func TestCallClassifiesWrappedErrorsByCause(t *testing.T) {
	tests := []struct {
		name     string
		cause    error
		wantType string
	}{
		{name: "canceled", cause: context.Canceled, wantType: "context.canceled"},
		{name: "deadline", cause: context.DeadlineExceeded, wantType: "context.deadline_exceeded"},
		{name: "invalid response", cause: chat.ErrInvalidResponse, wantType: "chat.invalid_response"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			middleware, rig := newRig(t, "openai")
			wrapped := fmt.Errorf("provider call: %w", test.cause)
			model := middleware.Call(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				return nil, wrapped
			}))
			if _, err := model.Call(t.Context(), request("gpt")); !errors.Is(err, test.cause) {
				t.Fatalf("Call error = %v, want cause %v", err, test.cause)
			}
			metrics := collectMetrics(t, rig.reader)
			assertMetricAttribute(t, metrics, "gen_ai.client.operation.duration", "error.type", test.wantType)
		})
	}
}

func TestStreamIsLazyAndObservesMetadata(t *testing.T) {
	middleware, rig := newRig(t, "openai")
	called := false
	streamer := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
		called = true
		return func(yield func(*chat.ResponseDelta, error) bool) {
			first := responseDelta("hel", "", 0, 0)
			first.Metadata.ID = ""
			first.Metadata.Model = ""
			second := responseDelta("lo", chat.FinishReasonStop, 9, 3)
			yield(first, nil)
			yield(second, nil)
		}
	})
	sequence := middleware.Stream(streamer).Stream(t.Context(), request("gpt-request"))
	if called || len(rig.spans.Ended()) != 0 {
		t.Fatal("stream instrumentation ran before iteration")
	}
	var chunks []*chat.ResponseDelta
	for chunk, err := range sequence {
		if err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, chunk)
	}
	if !called || len(chunks) != 2 || chunks[0].Text() != "hel" || chunks[1].Text() != "lo" {
		t.Fatalf("stream forwarding = called:%v chunks:%d", called, len(chunks))
	}
	span := rig.spans.Ended()[0]
	attrs := spanAttributes(t, span)
	assertStringAttr(t, attrs, "gen_ai.response.model", "served-model")
	if got := attrs["gen_ai.usage.output_tokens"].AsInt64(); got != 3 {
		t.Fatalf("stream output tokens = %d, want 3", got)
	}
	if !attrs["gen_ai.request.stream"].AsBool() {
		t.Fatal("missing streaming request attribute")
	}
	metrics := collectMetrics(t, rig.reader)
	if got := histogramInt64Sum(t, metrics, "gen_ai.client.token.usage", "gen_ai.token.type", "output"); got != 3 {
		t.Fatalf("stream output metric = %d, want 3", got)
	}
	assertMetricAttribute(t, metrics, "gen_ai.client.operation.time_to_first_chunk", "gen_ai.provider.name", "openai")
	assertMetricAttribute(t, metrics, "gen_ai.client.operation.time_to_first_chunk", "gen_ai.request.model", "gpt-request")
}

func TestStreamEndsSynchronouslyOnConsumerStop(t *testing.T) {
	middleware, rig := newRig(t, "openai")
	released := false
	streamer := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
		return func(yield func(*chat.ResponseDelta, error) bool) {
			defer func() { released = true }()
			if !yield(responseDelta("first", "", 0, 0), nil) {
				return
			}
			yield(responseDelta("second", chat.FinishReasonStop, 0, 0), nil)
		}
	})
	seen := 0
	middleware.Stream(streamer).Stream(t.Context(), request("gpt"))(func(*chat.ResponseDelta, error) bool {
		seen++
		return false
	})
	if !released || seen != 1 || len(rig.spans.Ended()) != 1 {
		t.Fatalf("released/seen/spans = %v/%d/%d", released, seen, len(rig.spans.Ended()))
	}
	if rig.spans.Ended()[0].Status().Code == codes.Error {
		t.Fatal("consumer stop must not be reported as provider failure")
	}
}

func TestStreamReportsNilAndProviderErrors(t *testing.T) {
	t.Run("nil sequence", func(t *testing.T) {
		middleware, rig := newRig(t, "openai")
		streamer := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] { return nil })
		var got error
		for _, err := range middleware.Stream(streamer).Stream(t.Context(), request("gpt")) {
			got = err
		}
		if !errors.Is(got, otelchat.ErrNilStream) || rig.spans.Ended()[0].Status().Code != codes.Error {
			t.Fatalf("error/status = %v/%v", got, rig.spans.Ended()[0].Status())
		}
	})

	t.Run("provider error", func(t *testing.T) {
		middleware, rig := newRig(t, "openai")
		want := errors.New("stream failed")
		streamer := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
			return func(yield func(*chat.ResponseDelta, error) bool) { yield(nil, want) }
		})
		var got error
		for _, err := range middleware.Stream(streamer).Stream(t.Context(), request("gpt")) {
			got = err
		}
		if !errors.Is(got, want) || rig.spans.Ended()[0].Status().Code != codes.Error {
			t.Fatalf("error/status = %v/%v", got, rig.spans.Ended()[0].Status())
		}
	})

	t.Run("partial response and provider error", func(t *testing.T) {
		middleware, rig := newRig(t, "openai")
		want := errors.New("stream failed after a partial response")
		partial := responseDelta("partial", chat.FinishReasonStop, 3, 1)
		streamer := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
			return func(yield func(*chat.ResponseDelta, error) bool) { yield(partial, want) }
		})
		var gotResponse *chat.ResponseDelta
		var gotErr error
		for chunk, err := range middleware.Stream(streamer).Stream(t.Context(), request("gpt")) {
			gotResponse, gotErr = chunk, err
		}
		if gotResponse != partial || !errors.Is(gotErr, want) {
			t.Fatalf("response/error = %p/%v, want %p/%v", gotResponse, gotErr, partial, want)
		}
		span := rig.spans.Ended()[0]
		assertStringAttr(t, spanAttributes(t, span), "gen_ai.response.model", "served-model")
		if span.Status().Code != codes.Error {
			t.Fatalf("span status = %v, want error", span.Status())
		}
	})
}

func TestStreamForwardsInvalidContentWithoutReassemblingIt(t *testing.T) {
	middleware, rig := newRig(t, "openai")
	invalid := &chat.ResponseDelta{}
	streamer := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
		return func(yield func(*chat.ResponseDelta, error) bool) { yield(invalid, nil) }
	})
	var got *chat.ResponseDelta
	for chunk, err := range middleware.Stream(streamer).Stream(t.Context(), request("gpt")) {
		if err != nil {
			t.Fatalf("observation changed business result: %v", err)
		}
		got = chunk
	}
	if got != invalid {
		t.Fatal("invalid provider chunk was replaced")
	}
	events := rig.spans.Ended()[0].Events()
	if len(events) != 0 {
		t.Fatalf("events = %v", events)
	}
}

func spanAttributes(t *testing.T, span sdktrace.ReadOnlySpan) map[string]attribute.Value {
	t.Helper()
	attrs := make(map[string]attribute.Value, len(span.Attributes()))
	for _, attr := range span.Attributes() {
		attrs[string(attr.Key)] = attr.Value
	}
	return attrs
}

func assertStringAttr(t *testing.T, attrs map[string]attribute.Value, key, want string) {
	t.Helper()
	value, ok := attrs[key]
	if !ok || value.AsString() != want {
		t.Fatalf("%s = %q (present %v), want %q", key, value.AsString(), ok, want)
	}
}

func collectMetrics(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()
	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &metrics); err != nil {
		t.Fatal(err)
	}
	return metrics
}

func histogramInt64Sum(
	t *testing.T,
	metrics metricdata.ResourceMetrics,
	name string,
	key attribute.Key,
	want string,
) int64 {
	t.Helper()
	for _, scope := range metrics.ScopeMetrics {
		for _, value := range scope.Metrics {
			if value.Name != name {
				continue
			}
			histogram, ok := value.Data.(metricdata.Histogram[int64])
			if !ok {
				t.Fatalf("metric %s is %T, want int64 histogram", name, value.Data)
			}
			var sum int64
			for _, point := range histogram.DataPoints {
				attr, found := point.Attributes.Value(key)
				if found && attr.AsString() == want {
					sum += point.Sum
				}
			}
			return sum
		}
	}
	t.Fatalf("metric %q not found", name)
	return 0
}

func assertMetricAttribute(
	t *testing.T,
	metrics metricdata.ResourceMetrics,
	name string,
	key attribute.Key,
	want string,
) {
	t.Helper()
	for _, scope := range metrics.ScopeMetrics {
		for _, value := range scope.Metrics {
			if value.Name != name {
				continue
			}
			switch data := value.Data.(type) {
			case metricdata.Histogram[int64]:
				for _, point := range data.DataPoints {
					if attr, ok := point.Attributes.Value(key); ok && attr.AsString() == want {
						return
					}
				}
			case metricdata.Histogram[float64]:
				for _, point := range data.DataPoints {
					if attr, ok := point.Attributes.Value(key); ok && attr.AsString() == want {
						return
					}
				}
			}
		}
	}
	t.Fatalf("metric %q has no %s=%q datapoint", name, key, want)
}
