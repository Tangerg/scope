package agent

import (
	"errors"
	"testing"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

func TestObserverPreservesInstrumentConstructionFailures(t *testing.T) {
	for _, name := range []string{
		processActivationsMetricName, processExitsMetricName,
		invokeAgentDurationMetricName, processCommittedStepsMetricName,
		processPreparedEffectsMetricName, processAcceptedSignalsMetricName,
		stepDurationMetricName, stepAdoptionDelayMetricName,
		effectDurationMetricName, deltaDropsMetricName,
		durabilityDurationMetricName, durabilitySnapshotBytesMetricName,
	} {
		t.Run(name, func(t *testing.T) {
			cause := errors.New("instrument configuration rejected")
			provider := rejectingMeterProvider{
				MeterProvider: noop.NewMeterProvider(), name: name, cause: cause,
			}
			observer, err := NewObserver(ObserverConfig{MeterProvider: provider})
			if observer != nil || !errors.Is(err, ErrInvalidObserverConfig) || !errors.Is(err, cause) {
				t.Fatalf("observer=%v error=%v, want both configuration and provider failures", observer, err)
			}
		})
	}
}

func TestObserverRejectsTypedNilMeterProvider(t *testing.T) {
	var provider *sdkmetric.MeterProvider
	observer, err := NewObserver(ObserverConfig{MeterProvider: provider})
	if observer != nil || !errors.Is(err, ErrInvalidObserverConfig) {
		t.Fatalf("observer=%v error=%v", observer, err)
	}
}

type rejectingMeterProvider struct {
	metric.MeterProvider
	name  string
	cause error
}

func (r rejectingMeterProvider) Meter(name string, options ...metric.MeterOption) metric.Meter {
	return rejectingMeter{Meter: r.MeterProvider.Meter(name, options...), name: r.name, cause: r.cause}
}

type rejectingMeter struct {
	metric.Meter
	name  string
	cause error
}

func (r rejectingMeter) Int64Counter(name string, options ...metric.Int64CounterOption) (metric.Int64Counter, error) {
	if name == r.name {
		return nil, r.cause
	}
	return r.Meter.Int64Counter(name, options...)
}

func (r rejectingMeter) Int64Histogram(name string, options ...metric.Int64HistogramOption) (metric.Int64Histogram, error) {
	if name == r.name {
		return nil, r.cause
	}
	return r.Meter.Int64Histogram(name, options...)
}

func (r rejectingMeter) Float64Histogram(name string, options ...metric.Float64HistogramOption) (metric.Float64Histogram, error) {
	if name == r.name {
		return nil, r.cause
	}
	return r.Meter.Float64Histogram(name, options...)
}
