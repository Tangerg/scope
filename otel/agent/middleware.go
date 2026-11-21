package agent

import (
	"context"
	"errors"
	"fmt"
	"iter"

	"github.com/samber/lo"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/otel/internal/instrument"
)

// MiddlewareConfig configures the instrumentation of a Runtime's physical
// I/O. Nil providers use the corresponding OpenTelemetry globals.
type MiddlewareConfig struct {
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
}

// Middleware instruments the physical I/O of a Runtime: its Store operations
// and every dispatch. The Observer separately projects the committed log. It
// is immutable after construction and safe for concurrent use.
type Middleware struct {
	tracer             trace.Tracer
	durabilityDuration metric.Float64Histogram
	durabilityBytes    metric.Int64Histogram
}

func NewMiddleware(config MiddlewareConfig) (Middleware, error) {
	meter := instrument.MeterProvider(config.MeterProvider).Meter(instrumentationName)
	duration, durationErr := meter.Float64Histogram(durabilityDurationMetricName, metric.WithUnit(durationUnit))
	bytes, bytesErr := meter.Int64Histogram(durabilityBatchBytesMetricName, metric.WithUnit(byteUnit))
	if err := errors.Join(durationErr, bytesErr); err != nil {
		return Middleware{}, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	return Middleware{
		tracer:             instrument.TracerProvider(config.TracerProvider).Tracer(instrumentationName),
		durabilityDuration: duration,
		durabilityBytes:    bytes,
	}, nil
}

// Store decorates a run.Store for Config.Store. Append, Load and Read pass
// through exactly while their duration and the proposed batch size are
// measured; a failed acknowledgment is never classified as rollback.
func (m Middleware) Store(next run.Store) run.Store {
	if lo.IsNil(next) {
		return nil
	}
	if err := m.validate(); err != nil {
		return invalidStore{err: err}
	}
	return &observedStore{middleware: m, next: next}
}

// Dispatch is a run.DispatchMiddleware. It spans each physical dispatch and
// propagates the span to downstream SDK calls. A dispatch repeated under the
// same EffectID produces another span.
func (m Middleware) Dispatch(next run.Dispatcher) run.Dispatcher {
	if lo.IsNil(next) {
		return nil
	}
	if err := m.validate(); err != nil {
		return run.DispatcherFunc(func(context.Context, run.EffectRequest, run.DeltaEmitter) (run.Settlement, error) {
			return run.Settlement{}, err
		})
	}
	return &observedDispatcher{middleware: m, next: next}
}

func (m Middleware) validate() error {
	if lo.IsNil(m.tracer) || lo.IsNil(m.durabilityDuration) || lo.IsNil(m.durabilityBytes) {
		return fmt.Errorf("%w: middleware must be constructed with NewMiddleware", ErrInvalidConfig)
	}
	return nil
}

// invalidStore is what an unconstructed Middleware decorates a Store into:
// every operation fails rather than silently passing through uninstrumented.
type invalidStore struct{ err error }

func (i invalidStore) Append(context.Context, run.Batch) error { return i.err }

func (i invalidStore) Load(context.Context, run.ProcessID) (run.Log, bool, error) {
	return run.Log{}, false, i.err
}

func (i invalidStore) Read(context.Context, run.ProcessID, run.LSN) iter.Seq2[run.Entry, error] {
	return func(yield func(run.Entry, error) bool) { yield(run.Entry{}, i.err) }
}
