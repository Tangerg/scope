package otel_test

import (
	"context"
	"errors"
	"iter"
	"slices"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	coreembedding "github.com/Tangerg/scope/core/embedding"
	coreimage "github.com/Tangerg/scope/core/image"
	coremoderation "github.com/Tangerg/scope/core/moderation"
	corererank "github.com/Tangerg/scope/core/rerank"
	corespeech "github.com/Tangerg/scope/core/speech"
	coretranscription "github.com/Tangerg/scope/core/transcription"
	otelembedding "github.com/Tangerg/scope/otel/embedding"
	otelimage "github.com/Tangerg/scope/otel/image"
	otelmoderation "github.com/Tangerg/scope/otel/moderation"
	otelrerank "github.com/Tangerg/scope/otel/rerank"
	otelspeech "github.com/Tangerg/scope/otel/speech"
	oteltranscription "github.com/Tangerg/scope/otel/transcription"
)

func TestClientMetricsDescribeOnlyObservedResponse(t *testing.T) {
	calls := []struct {
		name      string
		operation string
		call      func(context.Context, trace.TracerProvider, metric.MeterProvider, error) error
	}{
		{name: "embedding", operation: "embeddings", call: func(ctx context.Context, traces trace.TracerProvider, meters metric.MeterProvider, failure error) error {
			middleware, err := otelembedding.NewMiddleware(otelembedding.MiddlewareConfig{Provider: "provider", TracerProvider: traces, MeterProvider: meters})
			if err != nil {
				return err
			}
			wrapped, err := middleware.Wrap(coreembedding.ModelFunc(func(context.Context, *coreembedding.Request) (*coreembedding.Response, error) {
				return &coreembedding.Response{Metadata: &coreembedding.ResponseMetadata{Usage: &coreembedding.Usage{InputTokens: 3}}}, failure
			}))
			if err != nil {
				return err
			}
			_, err = wrapped.Call(ctx, &coreembedding.Request{Texts: []string{"input"}, Options: coreembedding.Options{Model: "requested", Dimensions: new(int64(8))}})
			return err
		}},
		{name: "image", operation: "generate_content", call: func(ctx context.Context, traces trace.TracerProvider, meters metric.MeterProvider, failure error) error {
			middleware, err := otelimage.NewMiddleware(otelimage.MiddlewareConfig{Provider: "provider", TracerProvider: traces, MeterProvider: meters})
			if err != nil {
				return err
			}
			wrapped, err := middleware.Wrap(coreimage.ModelFunc(func(context.Context, *coreimage.Request) (*coreimage.Response, error) {
				return &coreimage.Response{}, failure
			}))
			if err != nil {
				return err
			}
			_, err = wrapped.Call(ctx, &coreimage.Request{Prompt: "input", Options: coreimage.Options{Model: "requested"}})
			return err
		}},
		{name: "moderation", operation: "moderate", call: func(ctx context.Context, traces trace.TracerProvider, meters metric.MeterProvider, failure error) error {
			middleware, err := otelmoderation.NewMiddleware(otelmoderation.MiddlewareConfig{Provider: "provider", TracerProvider: traces, MeterProvider: meters})
			if err != nil {
				return err
			}
			wrapped, err := middleware.Wrap(coremoderation.ModelFunc(func(context.Context, *coremoderation.Request) (*coremoderation.Response, error) {
				return &coremoderation.Response{}, failure
			}))
			if err != nil {
				return err
			}
			_, err = wrapped.Call(ctx, &coremoderation.Request{Texts: []string{"input"}, Options: coremoderation.Options{Model: "requested"}})
			return err
		}},
		{name: "rerank", operation: "rerank", call: func(ctx context.Context, traces trace.TracerProvider, meters metric.MeterProvider, failure error) error {
			middleware, err := otelrerank.NewMiddleware(otelrerank.MiddlewareConfig{Provider: "provider", TracerProvider: traces, MeterProvider: meters})
			if err != nil {
				return err
			}
			wrapped, err := middleware.Wrap(corererank.ModelFunc(func(context.Context, *corererank.Request) (*corererank.Response, error) {
				return &corererank.Response{Metadata: &corererank.ResponseMetadata{Usage: &corererank.Usage{InputTokens: 3}}}, failure
			}))
			if err != nil {
				return err
			}
			_, err = wrapped.Call(ctx, &corererank.Request{Query: "input", Documents: []string{"document"}, Options: corererank.Options{Model: "requested"}})
			return err
		}},
		{name: "speech", operation: "synthesize_speech", call: func(ctx context.Context, traces trace.TracerProvider, meters metric.MeterProvider, failure error) error {
			middleware, err := otelspeech.NewMiddleware(otelspeech.MiddlewareConfig{Provider: "provider", TracerProvider: traces, MeterProvider: meters})
			if err != nil {
				return err
			}
			wrapped, err := middleware.Wrap(corespeech.ModelFunc(func(context.Context, *corespeech.Request) (*corespeech.Response, error) {
				return &corespeech.Response{}, failure
			}))
			if err != nil {
				return err
			}
			_, err = wrapped.Call(ctx, &corespeech.Request{Text: "input", Options: corespeech.Options{Model: "requested"}})
			return err
		}},
		{name: "transcription", operation: "transcribe", call: func(ctx context.Context, traces trace.TracerProvider, meters metric.MeterProvider, failure error) error {
			middleware, err := oteltranscription.NewMiddleware(oteltranscription.MiddlewareConfig{Provider: "provider", TracerProvider: traces, MeterProvider: meters})
			if err != nil {
				return err
			}
			wrapped, err := middleware.Wrap(coretranscription.ModelFunc(func(context.Context, *coretranscription.Request) (*coretranscription.Response, error) {
				return &coretranscription.Response{}, failure
			}))
			if err != nil {
				return err
			}
			_, err = wrapped.Call(ctx, &coretranscription.Request{Options: coretranscription.Options{Model: "requested"}})
			return err
		}},
	}
	for _, call := range calls {
		for _, failure := range []error{nil, errors.New("provider failed")} {
			name := "success"
			if failure != nil {
				name = "failure"
			}
			t.Run(call.name+"/"+name, func(t *testing.T) {
				spans := tracetest.NewSpanRecorder()
				traces := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
				reader := sdkmetric.NewManualReader()
				meters := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
				t.Cleanup(func() {
					if err := traces.Shutdown(context.Background()); err != nil {
						t.Error(err)
					}
					if err := meters.Shutdown(context.Background()); err != nil {
						t.Error(err)
					}
				})
				if err := call.call(t.Context(), traces, meters, failure); !errors.Is(err, failure) {
					t.Fatalf("Call error = %v, want %v", err, failure)
				}
				ended := spans.Ended()
				if len(ended) != 1 {
					t.Fatalf("spans = %d, want 1", len(ended))
				}
				span := ended[0]
				if span.Name() != call.operation+" requested" {
					t.Errorf("span name = %q", span.Name())
				}
				attributes := attribute.NewSet(span.Attributes()...)
				if value, found := attributes.Value("gen_ai.response.model"); found {
					t.Errorf("invented response model %v", value)
				}
				outputTypes := map[string]string{"image": "image", "speech": "speech", "transcription": "text"}
				if want := outputTypes[call.name]; want != "" {
					if value, _ := attributes.Value("gen_ai.output.type"); value.AsString() != want {
						t.Errorf("output.type = %v, want %s", value, want)
					}
				}
				if call.name == "embedding" {
					if value, _ := attributes.Value("gen_ai.embeddings.dimension.count"); value.AsInt64() != 8 {
						t.Errorf("embedding dimensions = %v", value)
					}
				}
				var metrics metricdata.ResourceMetrics
				if err := reader.Collect(t.Context(), &metrics); err != nil {
					t.Fatal(err)
				}
				duration := clientMetric(t, metrics, "gen_ai.client.operation.duration")
				point := duration.Data.(metricdata.Histogram[float64]).DataPoints[0]
				if point.Sum != span.EndTime().Sub(span.StartTime()).Seconds() {
					t.Error("client duration does not match span duration")
				}
				if !slices.Equal(point.Bounds, []float64{0.01, 0.02, 0.04, 0.08, 0.16, 0.32, 0.64, 1.28, 2.56, 5.12, 10.24, 20.48, 40.96, 81.92}) {
					t.Errorf("duration bounds = %v", point.Bounds)
				}
				if value, found := point.Attributes.Value("gen_ai.response.model"); found {
					t.Errorf("invented response model metric %v", value)
				}
				if _, found := point.Attributes.Value("error.type"); found != (failure != nil) {
					t.Error("duration error classification disagrees with outcome")
				}
				if call.name != "embedding" && call.name != "rerank" {
					return
				}
				if value, _ := attributes.Value("gen_ai.usage.input_tokens"); value.AsInt64() != 3 {
					t.Errorf("span input tokens = %v", value)
				}
				usage := clientMetric(t, metrics, "gen_ai.client.token.usage")
				tokenPoint := usage.Data.(metricdata.Histogram[int64]).DataPoints[0]
				if tokenPoint.Sum != 3 || tokenPoint.Count != 1 {
					t.Errorf("token sum/count = %d/%d, want 3/1", tokenPoint.Sum, tokenPoint.Count)
				}
				if _, found := tokenPoint.Attributes.Value("error.type"); found {
					t.Error("token usage incorrectly has error.type dimension")
				}
				if !slices.Equal(tokenPoint.Bounds, []float64{1, 4, 16, 64, 256, 1024, 4096, 16384, 65536, 262144, 1048576, 4194304, 16777216, 67108864}) {
					t.Errorf("token bounds = %v", tokenPoint.Bounds)
				}
			})
		}
	}
}

func clientMetric(t *testing.T, metrics metricdata.ResourceMetrics, name string) metricdata.Metrics {
	t.Helper()
	for _, scope := range metrics.ScopeMetrics {
		for _, instrument := range scope.Metrics {
			if instrument.Name == name {
				return instrument
			}
		}
	}
	t.Fatalf("metric %s missing", name)
	return metricdata.Metrics{}
}

func TestSpeechStreamRecordsChunkLatency(t *testing.T) {
	for _, stopAfter := range []int{1, 3} {
		t.Run(strconv.Itoa(stopAfter)+" chunks", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				spans := tracetest.NewSpanRecorder()
				traces := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
				reader := sdkmetric.NewManualReader()
				meters := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
				t.Cleanup(func() {
					if err := traces.Shutdown(context.Background()); err != nil {
						t.Error(err)
					}
					if err := meters.Shutdown(context.Background()); err != nil {
						t.Error(err)
					}
				})
				middleware, err := otelspeech.NewMiddleware(otelspeech.MiddlewareConfig{Provider: "provider", TracerProvider: traces, MeterProvider: meters})
				if err != nil {
					t.Fatal(err)
				}
				closed := false
				streamer, err := middleware.WrapStream(corespeech.StreamerFunc(func(context.Context, *corespeech.Request) iter.Seq2[*corespeech.Response, error] {
					return func(yield func(*corespeech.Response, error) bool) {
						defer func() { closed = true }()
						for _, delay := range []time.Duration{time.Second, 2 * time.Second, 3 * time.Second} {
							time.Sleep(delay)
							response := &corespeech.Response{Output: &corespeech.Output{Audio: []byte("audio")}, Metadata: &corespeech.ResponseMetadata{Model: "served"}}
							if !yield(response, nil) {
								return
							}
						}
					}
				}))
				if err != nil {
					t.Fatal(err)
				}
				count := 0
				for _, streamErr := range streamer.Stream(t.Context(), &corespeech.Request{Text: "input", Options: corespeech.Options{Model: "requested"}}) {
					if streamErr != nil {
						t.Fatal(streamErr)
					}
					count++
					if count == stopAfter {
						break
					}
				}
				if !closed {
					t.Error("stream resources were not released synchronously")
				}
				span := spans.Ended()[0]
				attributes := attribute.NewSet(span.Attributes()...)
				if value, _ := attributes.Value("gen_ai.request.stream"); !value.AsBool() {
					t.Error("streaming request not identified")
				}
				if value, _ := attributes.Value("gen_ai.response.time_to_first_chunk"); value.AsFloat64() != 1 {
					t.Errorf("first chunk latency = %v, want 1", value)
				}
				var metrics metricdata.ResourceMetrics
				if err := reader.Collect(t.Context(), &metrics); err != nil {
					t.Fatal(err)
				}
				first := clientMetric(t, metrics, "gen_ai.client.operation.time_to_first_chunk").Data.(metricdata.Histogram[float64]).DataPoints[0]
				if first.Sum != 1 || first.Count != 1 {
					t.Errorf("first chunk sum/count = %v/%d", first.Sum, first.Count)
				}
				elapsed := float64(1)
				if stopAfter == 3 {
					elapsed = 6
					interval := clientMetric(t, metrics, "gen_ai.client.operation.time_per_output_chunk").Data.(metricdata.Histogram[float64]).DataPoints[0]
					if interval.Sum != 5 || interval.Count != 2 {
						t.Errorf("interval sum/count = %v/%d, want 5/2", interval.Sum, interval.Count)
					}
				}
				duration := clientMetric(t, metrics, "gen_ai.client.operation.duration").Data.(metricdata.Histogram[float64]).DataPoints[0]
				if duration.Sum != elapsed || span.EndTime().Sub(span.StartTime()).Seconds() != elapsed {
					t.Errorf("stream duration = %v, want %v", duration.Sum, elapsed)
				}
			})
		})
	}
}
