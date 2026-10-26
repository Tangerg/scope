package slog

import (
	"encoding/json/jsontext"
	stdslog "log/slog"
	"math"

	"go.opentelemetry.io/otel/attribute"
)

const (
	attributesKey   = "attributes"
	traceIDKey      = "trace_id"
	spanIDKey       = "span_id"
	parentSpanIDKey = "parent_span_id"
	scopeKey        = "scope"
	eventNameKey    = "event_name"
	spanNameKey     = "name"
	durationKey     = "duration"
	eventsKey       = "events"
	metricNameKey   = "metric"
	metricUnitKey   = "unit"
	metricValueKey  = "value"
)

func attributeToSlog(value attribute.KeyValue) stdslog.Attr {
	key := string(value.Key)
	switch kind := value.Value.Type(); kind {
	case attribute.MAP, attribute.SLICE, attribute.BOOLSLICE, attribute.INT64SLICE, attribute.FLOAT64SLICE, attribute.STRINGSLICE:
		// The SDK owns non-OTLP encoding, including nested empty values and
		// nonfinite numbers; JSON handlers must retain that structure.
		return stdslog.Any(key, jsontext.Value(value.Value.String()))
	case attribute.BYTESLICE:
		return stdslog.String(key, value.Value.String())
	case attribute.FLOAT64:
		if number := value.Value.AsFloat64(); math.IsNaN(number) || math.IsInf(number, 0) {
			return stdslog.String(key, value.Value.String())
		}
	}
	return stdslog.Any(key, value.Value.AsInterface())
}
