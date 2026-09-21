package speech_test

import (
	"context"
	"errors"
	"iter"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	corespeech "github.com/Tangerg/scope/core/speech"
	otelspeech "github.com/Tangerg/scope/otel/speech"
)

func TestStreamRemainsLazyAndDoesNotObserveContent(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	traces := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(spans))
	meters := sdkmetric.NewMeterProvider()
	t.Cleanup(func() {
		_ = traces.Shutdown(context.Background())
		_ = meters.Shutdown(context.Background())
	})
	middleware, err := otelspeech.NewMiddleware(otelspeech.MiddlewareConfig{
		Provider: "provider", TracerProvider: traces, MeterProvider: meters,
	})
	if err != nil {
		t.Fatal(err)
	}
	started := false
	streamer, err := middleware.WrapStream(corespeech.StreamerFunc(func(context.Context, *corespeech.Request) iter.Seq2[*corespeech.Response, error] {
		started = true
		return func(yield func(*corespeech.Response, error) bool) {
			yield(&corespeech.Response{
				Output:   &corespeech.Output{Audio: []byte("sensitive audio")},
				Metadata: &corespeech.ResponseMetadata{Model: "served"},
			}, nil)
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	request, _ := corespeech.NewRequest("sensitive text")
	request.Options.Model = "requested"
	sequence := streamer.Stream(t.Context(), request)
	if started || len(spans.Ended()) != 0 {
		t.Fatal("stream began before iteration")
	}
	for response, streamErr := range sequence {
		if streamErr != nil || response == nil {
			t.Fatalf("Stream yielded (%v, %v)", response, streamErr)
		}
		break
	}
	if !started || len(spans.Ended()) != 1 {
		t.Fatalf("started/spans = %t/%d", started, len(spans.Ended()))
	}
	if len(spans.Ended()) == 0 {
		t.Fatal("expected a recorded span")
	}
	for _, value := range spans.Ended()[0].Attributes() {
		if value.Value.AsString() == request.Text || value.Value.AsString() == "sensitive audio" {
			t.Fatal("speech content leaked into telemetry")
		}
	}
}

func TestMiddlewareRejectsMissingCapabilities(t *testing.T) {
	middleware, err := otelspeech.NewMiddleware(otelspeech.MiddlewareConfig{Provider: "provider"})
	if err != nil {
		t.Fatal(err)
	}
	var model corespeech.ModelFunc
	if _, err := middleware.Wrap(model); !errors.Is(err, otelspeech.ErrInvalidModel) {
		t.Fatalf("Wrap error = %v", err)
	}
	var streamer corespeech.StreamerFunc
	if _, err := middleware.WrapStream(streamer); !errors.Is(err, otelspeech.ErrInvalidStreamer) {
		t.Fatalf("WrapStream error = %v", err)
	}
}
