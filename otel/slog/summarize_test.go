package slog

import (
	"fmt"
	stdslog "log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestSummarizeRendersEveryAggregation(t *testing.T) {
	exporter := NewMetricExporter(stdslog.Default())
	cases := map[string]struct {
		data metricdata.Aggregation
		want string
	}{
		"int sum": {
			data: metricdata.Sum[int64]{DataPoints: []metricdata.DataPoint[int64]{{Value: 2}, {Value: 3}}},
			want: "sum=5 points=2",
		},
		"float sum": {
			data: metricdata.Sum[float64]{DataPoints: []metricdata.DataPoint[float64]{{Value: 1.5}, {Value: 0.5}}},
			want: "sum=2 points=2",
		},
		"int gauge": {
			data: metricdata.Gauge[int64]{DataPoints: []metricdata.DataPoint[int64]{{Value: 1}, {Value: 7}}},
			want: "gauge=7 points=2",
		},
		"float gauge": {
			data: metricdata.Gauge[float64]{DataPoints: []metricdata.DataPoint[float64]{{Value: 2.5}}},
			want: "gauge=2.5 points=1",
		},
		"empty gauge": {
			data: metricdata.Gauge[int64]{},
			want: "gauge=<none>",
		},
		"int histogram": {
			data: metricdata.Histogram[int64]{DataPoints: []metricdata.HistogramDataPoint[int64]{
				{Count: 2, Sum: 10},
				{Count: 3, Sum: 5},
			}},
			want: "hist count=5 sum=15 points=2",
		},
		"float histogram": {
			data: metricdata.Histogram[float64]{DataPoints: []metricdata.HistogramDataPoint[float64]{
				{Count: 1, Sum: 2.5},
			}},
			want: "hist count=1 sum=2.5 points=1",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			if got := exporter.summarize(testCase.data); got != testCase.want {
				t.Fatalf("summarize = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestSummarizeFallsBackForAnUnknownAggregation(t *testing.T) {
	exporter := NewMetricExporter(stdslog.Default())
	got := exporter.summarize(metricdata.ExponentialHistogram[int64]{})
	if got == "" || !strings.Contains(got, "ExponentialHistogram") {
		t.Fatalf("summarize = %q, want the aggregation type name", got)
	}
}

func TestSelectorsDeferToTheSDK(t *testing.T) {
	exporter := NewMetricExporter(stdslog.Default())
	kinds := []sdkmetric.InstrumentKind{
		sdkmetric.InstrumentKindCounter,
		sdkmetric.InstrumentKindUpDownCounter,
		sdkmetric.InstrumentKindHistogram,
		sdkmetric.InstrumentKindGauge,
		sdkmetric.InstrumentKindObservableCounter,
		sdkmetric.InstrumentKindObservableUpDownCounter,
		sdkmetric.InstrumentKindObservableGauge,
	}
	for _, kind := range kinds {
		if got, want := exporter.Temporality(kind), sdkmetric.DefaultTemporalitySelector(kind); got != want {
			t.Errorf("Temporality(%v) = %v, want the SDK default %v", kind, got, want)
		}
		got := exporter.Aggregation(kind)
		want := sdkmetric.DefaultAggregationSelector(kind)
		if fmt.Sprintf("%#v", got) != fmt.Sprintf("%#v", want) {
			t.Errorf("Aggregation(%v) = %#v, want the SDK default %#v", kind, got, want)
		}
	}
}

func TestSeverityMapsOntoTheNearestSlogLevel(t *testing.T) {
	cases := map[otellog.Severity]stdslog.Level{
		otellog.SeverityUndefined: stdslog.LevelDebug,
		otellog.SeverityTrace:     stdslog.LevelDebug,
		otellog.SeverityDebug:     stdslog.LevelDebug,
		otellog.SeverityDebug4:    stdslog.LevelDebug,
		otellog.SeverityInfo:      stdslog.LevelInfo,
		otellog.SeverityInfo4:     stdslog.LevelInfo,
		otellog.SeverityWarn:      stdslog.LevelWarn,
		otellog.SeverityWarn4:     stdslog.LevelWarn,
		otellog.SeverityError:     stdslog.LevelError,
		otellog.SeverityError4:    stdslog.LevelError,
		otellog.SeverityFatal:     stdslog.LevelError,
		otellog.SeverityFatal4:    stdslog.LevelError,
	}
	for severity, want := range cases {
		if got := severityToLevel(severity); got != want {
			t.Errorf("severityToLevel(%v) = %v, want %v", severity, got, want)
		}
	}
}

func TestLogAttributesStayTyped(t *testing.T) {
	cases := map[string]struct {
		value attribute.KeyValue
		kind  stdslog.Kind
	}{
		"bool":    {value: attribute.Bool("k", true), kind: stdslog.KindBool},
		"int64":   {value: attribute.Int64("k", 7), kind: stdslog.KindInt64},
		"float64": {value: attribute.Float64("k", 1.5), kind: stdslog.KindFloat64},
		"string":  {value: attribute.String("k", "v"), kind: stdslog.KindString},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			attr := logKVToSlog(testCase.value)
			if attr.Key != "k" {
				t.Fatalf("key = %q", attr.Key)
			}
			if attr.Value.Kind() != testCase.kind {
				t.Fatalf("kind = %v, want %v", attr.Value.Kind(), testCase.kind)
			}
		})
	}

	composite := logKVToSlog(attribute.StringSlice("k", []string{"a", "b"}))
	if composite.Value.Kind() != stdslog.KindString {
		t.Fatalf("a composite value rendered as %v, want a string fallback", composite.Value.Kind())
	}
	if !strings.Contains(composite.Value.String(), "a") {
		t.Fatalf("the composite fallback lost its content: %q", composite.Value.String())
	}
}
