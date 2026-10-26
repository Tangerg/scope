package slog_test

import (
	"bytes"
	"context"
	jsonv2 "encoding/json/v2"
	stdslog "log/slog"
	"math"
	"reflect"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/Tangerg/scope/otel/slog"
)

func TestExportersPreserveStructuredAttributeValues(t *testing.T) {
	attributes := []attribute.KeyValue{
		attribute.StringSlice("strings", []string{"a", "b"}),
		attribute.BoolSlice("booleans", []bool{true, false}),
		attribute.Int64Slice("integers", []int64{1, 2}),
		attribute.Float64Slice("floats", []float64{1.5, math.Inf(1)}),
		{Key: "map", Value: attribute.MapValue(attribute.String("field", "value"))},
		{Key: "nested", Value: attribute.SliceValue(attribute.MapValue(attribute.Int64("n", 3)), attribute.Value{})},
		{Key: "bytes", Value: attribute.ByteSliceValue([]byte{0, 1, 255})},
		{Key: "empty", Value: attribute.Value{}},
		attribute.Float64("nonfinite", math.NaN()),
	}
	want := map[string]any{
		"strings": []any{"a", "b"}, "booleans": []any{true, false},
		"integers": []any{float64(1), float64(2)}, "floats": []any{1.5, "Infinity"},
		"map":    map[string]any{"field": "value"},
		"nested": []any{map[string]any{"n": float64(3)}, nil},
		"bytes":  "AAH/", "empty": nil, "nonfinite": "NaN",
	}
	for _, signal := range []string{"span", "log"} {
		t.Run(signal, func(t *testing.T) {
			var output bytes.Buffer
			logger := stdslog.New(stdslog.NewJSONHandler(&output, nil))
			var err error
			if signal == "span" {
				err = slog.NewSpanExporter(logger).ExportSpans(t.Context(), []sdktrace.ReadOnlySpan{
					tracetest.SpanStub{Attributes: attributes}.Snapshot(),
				})
			} else {
				provider := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(slog.NewLogExporter(logger))))
				t.Cleanup(func() {
					if shutdownErr := provider.Shutdown(context.Background()); shutdownErr != nil {
						t.Error(shutdownErr)
					}
				})
				var record otellog.Record
				record.AddAttributes(attributes...)
				record.SetSeverity(otellog.SeverityInfo)
				provider.Logger("test").Emit(t.Context(), record)
			}
			if err != nil {
				t.Fatal(err)
			}
			var record struct {
				Attributes map[string]any `json:"attributes"`
			}
			if err := jsonv2.Unmarshal(output.Bytes(), &record); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(record.Attributes, want) {
				t.Fatalf("structured attributes lost their representation: got %#v, want %#v", record.Attributes, want)
			}
		})
	}
}
