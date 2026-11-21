package instrument

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/Tangerg/scope/otel/errortelemetry"
)

func TestMeasurementRecordsOneIntervalAndItsFailure(t *testing.T) {
	for _, failure := range []error{nil, errors.New("backend failed")} {
		spans := tracetest.NewSpanRecorder()
		tracer := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)).Tracer("test")
		reader := sdkmetric.NewManualReader()
		duration, err := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test").Float64Histogram("duration")
		if err != nil {
			t.Fatal(err)
		}
		operation := attribute.String("operation", "read")
		ctx, measurement := StartMeasurement(context.Background(), tracer, "read", trace.SpanKindClient, operation)
		measurement.End(ctx, duration, errortelemetry.Classify(failure), operation)

		span := spans.Ended()[0]
		var metrics metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &metrics); err != nil {
			t.Fatal(err)
		}
		point := metrics.ScopeMetrics[0].Metrics[0].Data.(metricdata.Histogram[float64]).DataPoints[0]
		if got, want := point.Sum, span.EndTime().Sub(span.StartTime()).Seconds(); got != want {
			t.Fatalf("duration = %v, span interval = %v", got, want)
		}
		_, classified := point.Attributes.Value(semconv.ErrorTypeKey)
		if failed := span.Status().Code == codes.Error; classified != (failure != nil) || failed != (failure != nil) {
			t.Fatalf("failure %v: duration classified %t, span failed %t", failure, classified, failed)
		}
	}
}
