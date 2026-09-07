package chat_test

import (
	"context"
	"errors"
	"iter"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/Tangerg/scope/core/chat"
)

func TestCallDoesNotExportErrorText(t *testing.T) {
	middleware, rig := newRig(t, "openai")
	const secret = "private-provider-payload"
	want := errors.New(secret)
	_, err := middleware.Call(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return nil, want
	})).Call(t.Context(), request("model"))
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want original provider error", err)
	}
	span := rig.spans.Ended()[0]
	if strings.Contains(span.Status().Description, secret) {
		t.Error("provider error text leaked through span status")
	}
	for _, event := range span.Events() {
		for _, value := range event.Attributes {
			if strings.Contains(value.Value.String(), secret) {
				t.Errorf("provider error text leaked through %s", value.Key)
			}
		}
	}
}

func TestStreamMeasuresEveryReceivedChunk(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		middleware, rig := newRig(t, "openai")
		streamer := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
			return func(yield func(*chat.ResponseDelta, error) bool) {
				time.Sleep(time.Second)
				if !yield(&chat.ResponseDelta{Metadata: &chat.ResponseMetadata{Model: "served"}}, nil) {
					return
				}
				time.Sleep(2 * time.Second)
				if !yield(&chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta("private response")}}, nil) {
					return
				}
				time.Sleep(3 * time.Second)
				yield(&chat.ResponseDelta{FinishReason: chat.FinishReasonStop}, nil)
			}
		})
		for _, err := range middleware.Stream(streamer).Stream(t.Context(), request("requested")) {
			if err != nil {
				t.Fatal(err)
			}
		}
		span := rig.spans.Ended()[0]
		attributes := spanAttributes(t, span)
		if !attributes["gen_ai.request.stream"].AsBool() {
			t.Error("streaming request is not identified")
		}
		if got := attributes["gen_ai.response.time_to_first_chunk"].AsFloat64(); got != 1 {
			t.Errorf("first chunk latency = %g, want 1 second", got)
		}
		metrics := collectMetrics(t, rig.reader)
		assertFloatHistogram(t, metrics, "gen_ai.client.operation.time_to_first_chunk", 1, 1)
		assertFloatHistogram(t, metrics, "gen_ai.client.operation.time_per_output_chunk", 2, 5)
		assertFloatHistogram(t, metrics, "gen_ai.client.operation.duration", 1, 6)
		if got := span.EndTime().Sub(span.StartTime()); got != 6*time.Second {
			t.Errorf("span duration = %v, want 6s", got)
		}
	})
}

func TestStreamPreservesKnownUsageWithoutCompleteContent(t *testing.T) {
	middleware, rig := newRig(t, "openai")
	wantErr := errors.New("generation interrupted")
	partial := responseDelta("partial", "", 9, 3)
	partial.Metadata.Usage.CacheReadInputTokens = new(int64(2))
	partial.Metadata.Usage.CacheWriteInputTokens = new(int64(0))
	partial.Metadata.Usage.ReasoningTokens = new(int64(1))
	streamer := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
		return func(yield func(*chat.ResponseDelta, error) bool) {
			if yield(partial, nil) {
				yield(nil, wantErr)
			}
		}
	})
	var received error
	for _, err := range middleware.Stream(streamer).Stream(t.Context(), request("requested")) {
		received = err
	}
	if !errors.Is(received, wantErr) {
		t.Fatalf("error = %v, want original error", received)
	}
	attributes := spanAttributes(t, rig.spans.Ended()[0])
	assertStringAttr(t, attributes, "gen_ai.response.model", "served-model")
	assertStringAttr(t, attributes, "error.type", "*errors.errorString")
	if got := attributes["gen_ai.response.finish_reasons"].AsStringSlice(); !slices.Equal(got, []string{"error"}) {
		t.Errorf("finish reasons = %v, want [error]", got)
	}
	for key, want := range map[string]int64{
		"gen_ai.usage.input_tokens": 9, "gen_ai.usage.output_tokens": 3,
		"gen_ai.usage.cache_read.input_tokens": 2, "gen_ai.usage.cache_write.input_tokens": 0,
		"gen_ai.usage.reasoning.output_tokens": 1,
	} {
		if value, found := attributes[key]; !found || value.AsInt64() != want {
			t.Errorf("%s = %v (present %v), want %d", key, value, found, want)
		}
	}
	metrics := collectMetrics(t, rig.reader)
	if got := histogramInt64Sum(t, metrics, "gen_ai.client.token.usage", "gen_ai.token.type", "input"); got != 9 {
		t.Errorf("input tokens = %d, want 9", got)
	}
	if got := histogramInt64Sum(t, metrics, "gen_ai.client.token.usage", "gen_ai.token.type", "output"); got != 3 {
		t.Errorf("output tokens = %d, want 3", got)
	}
	for _, scope := range metrics.ScopeMetrics {
		for _, value := range scope.Metrics {
			if value.Name != "gen_ai.client.token.usage" {
				continue
			}
			for _, point := range value.Data.(metricdata.Histogram[int64]).DataPoints {
				if point.Attributes.HasValue("error.type") {
					t.Error("token usage must not split series by errors")
				}
			}
		}
	}
}

func TestCallRecordsRequestedOutputTypeWithoutGuessingResponseModel(t *testing.T) {
	for _, test := range []struct {
		format chat.OutputFormatType
		want   string
	}{
		{chat.OutputFormatText, "text"},
		{chat.OutputFormatJSON, "json"},
		{chat.OutputFormatJSONSchema, "json"},
	} {
		t.Run(string(test.format), func(t *testing.T) {
			middleware, rig := newRig(t, "openai")
			input := request("model-alias")
			input.Options.OutputFormat = &chat.OutputFormat{Type: test.format}
			if test.format == chat.OutputFormatJSONSchema {
				input.Options.OutputFormat.Name = "result"
				input.Options.OutputFormat.Schema = []byte(`{"type":"object"}`)
			}
			output := response("done", chat.FinishReasonStop, 4, 2)
			output.Metadata.Model = ""
			_, err := middleware.Call(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				return output, nil
			})).Call(t.Context(), input)
			if err != nil {
				t.Fatal(err)
			}
			attributes := spanAttributes(t, rig.spans.Ended()[0])
			assertStringAttr(t, attributes, "gen_ai.output.type", test.want)
			metrics := collectMetrics(t, rig.reader)
			for _, scope := range metrics.ScopeMetrics {
				for _, value := range scope.Metrics {
					if histogram, ok := value.Data.(metricdata.Histogram[float64]); ok {
						for _, point := range histogram.DataPoints {
							if point.Attributes.HasValue("gen_ai.response.model") {
								t.Error("requested alias was invented as the actual response model")
							}
						}
					}
				}
			}
		})
	}
}

func assertFloatHistogram(t *testing.T, metrics metricdata.ResourceMetrics, name string, count uint64, sum float64) {
	t.Helper()
	for _, scope := range metrics.ScopeMetrics {
		for _, value := range scope.Metrics {
			if value.Name != name {
				continue
			}
			histogram, ok := value.Data.(metricdata.Histogram[float64])
			if !ok || len(histogram.DataPoints) != 1 {
				t.Fatalf("%s = %v, want one float histogram series", name, value.Data)
			}
			point := histogram.DataPoints[0]
			if value.Unit != "s" || point.Count != count || point.Sum != sum {
				t.Errorf("%s unit/count/sum = %s/%d/%g, want s/%d/%g", name, value.Unit, point.Count, point.Sum, count, sum)
			}
			wantBounds := []float64{0.01, 0.02, 0.04, 0.08, 0.16, 0.32, 0.64, 1.28, 2.56, 5.12, 10.24, 20.48, 40.96, 81.92}
			if !slices.Equal(point.Bounds, wantBounds) {
				t.Errorf("%s boundaries = %v, want %v", name, point.Bounds, wantBounds)
			}
			return
		}
	}
	t.Fatalf("metric %q not found", name)
}
