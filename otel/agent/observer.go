package agent

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sync"

	"github.com/samber/lo"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/otel/internal/instrument"
)

const (
	instrumentationName               = "github.com/Tangerg/scope/otel/agent"
	durationUnit                      = "s"
	byteUnit                          = "By"
	processStartsMetricName           = "agent.process.starts"
	processExitsMetricName            = "agent.process.exits"
	invokeAgentDurationMetricName     = "gen_ai.invoke_agent.duration"
	processCommittedStepsMetricName   = "agent.process.committed_steps"
	processPreparedEffectsMetricName  = "agent.process.prepared_effects"
	processAcceptedMessagesMetricName = "agent.process.accepted_messages"
	effectDurationMetricName          = "agent.effect.duration"
	durabilityDurationMetricName      = "agent.log.operation.duration"
	durabilityBatchBytesMetricName    = "agent.log.batch.bytes"
)

var (
	ErrInvalidObserverConfig = errors.New("agent otel: invalid observer configuration")
	ErrObservationClosed     = errors.New("agent otel: observer is closed")
	ErrObservationSequence   = errors.New("agent otel: noncontiguous log observation")
)

// Nil providers use the corresponding OpenTelemetry globals.
type ObserverConfig struct {
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
}

// LogSource deliberately exposes no control method: telemetry cannot advance
// a Process, acknowledge an Effect, or acquire a writer epoch.
type LogSource interface {
	ID() run.ProcessID
	Observe(context.Context, run.LSN) iter.Seq2[run.Entry, error]
}

type Observer struct {
	tracer      trace.Tracer
	instruments observerInstruments
	mu          sync.Mutex
	closed      bool
	readers     map[*logProjection]context.CancelFunc
}

func NewObserver(config ObserverConfig) (*Observer, error) {
	instruments, err := newObserverInstruments(instrument.MeterProvider(config.MeterProvider).Meter(instrumentationName))
	if err != nil {
		return nil, err
	}
	return &Observer{tracer: instrument.TracerProvider(config.TracerProvider).Tracer(instrumentationName), instruments: instruments, readers: make(map[*logProjection]context.CancelFunc)}, nil
}

// Consume returns the last fully projected LSN, including on cancellation.
// A resumed reader records only new facts; missing historical span starts are
// not fabricated. The caller persists the cursor and owns reader scheduling.
func (o *Observer) Consume(ctx context.Context, source LogSource, after run.LSN) (run.LSN, error) {
	if ctx == nil || o == nil || o.tracer == nil || lo.IsNil(source) || !source.ID().Valid() || source.ID().Depth() != 0 {
		return after, ErrInvalidObserverConfig
	}
	ctx, cancel := context.WithCancel(ctx)
	projection := newLogProjection(o, source.ID())
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		cancel()
		return after, ErrObservationClosed
	}
	o.readers[projection] = cancel
	o.mu.Unlock()
	defer func() {
		cancel()
		projection.close()
		o.mu.Lock()
		delete(o.readers, projection)
		close(projection.done)
		o.mu.Unlock()
	}()
	for entry, err := range source.Observe(ctx, after) {
		if err != nil {
			return after, err
		}
		if entry.LSN == 0 || entry.LSN != after+1 {
			return after, ErrObservationSequence
		}
		if err := entry.ValidateFor(projection.root); err != nil {
			return after, err
		}
		if err := projection.apply(ctx, entry); err != nil {
			return after, err
		}
		after = entry.LSN
	}
	return after, nil
}

func (o *Observer) Close(ctx context.Context) error {
	if ctx == nil || o == nil || o.tracer == nil {
		return ErrInvalidObserverConfig
	}
	o.mu.Lock()
	o.closed = true
	readers := make([]<-chan struct{}, 0, len(o.readers))
	for projection, cancel := range o.readers {
		cancel()
		readers = append(readers, projection.done)
	}
	o.mu.Unlock()
	for _, done := range readers {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (o *Observer) enabled() bool {
	if o == nil || o.tracer == nil {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return !o.closed
}

func (o *Observer) WrapStore(next run.Store) (run.Store, error) {
	if !o.enabled() || lo.IsNil(next) {
		return nil, ErrInvalidObserverConfig
	}
	return &observedStore{observer: o, next: next}, nil
}

func (o *Observer) WrapDispatcher(next run.Dispatcher) (run.Dispatcher, error) {
	if !o.enabled() || lo.IsNil(next) {
		return nil, ErrInvalidObserverConfig
	}
	return &observedDispatcher{observer: o, next: next}, nil
}

type observerInstruments struct {
	processStarts      metric.Int64Counter
	processExits       metric.Int64Counter
	processDuration    metric.Float64Histogram
	steps              metric.Int64Counter
	effects            metric.Int64Counter
	messages           metric.Int64Counter
	effectDuration     metric.Float64Histogram
	durabilityDuration metric.Float64Histogram
	durabilityBytes    metric.Int64Histogram
}

func newObserverInstruments(meter metric.Meter) (observerInstruments, error) {
	var instruments observerInstruments
	var failures []error
	counter := func(name string, target *metric.Int64Counter) {
		value, err := meter.Int64Counter(name)
		if err != nil {
			failures = append(failures, err)
		}
		*target = value
	}
	histogram := func(name string, target *metric.Float64Histogram) {
		value, err := meter.Float64Histogram(name, metric.WithUnit(durationUnit))
		if err != nil {
			failures = append(failures, err)
		}
		*target = value
	}
	counter(processStartsMetricName, &instruments.processStarts)
	counter(processExitsMetricName, &instruments.processExits)
	counter(processCommittedStepsMetricName, &instruments.steps)
	counter(processPreparedEffectsMetricName, &instruments.effects)
	counter(processAcceptedMessagesMetricName, &instruments.messages)
	histogram(invokeAgentDurationMetricName, &instruments.processDuration)
	histogram(effectDurationMetricName, &instruments.effectDuration)
	histogram(durabilityDurationMetricName, &instruments.durabilityDuration)
	var err error
	instruments.durabilityBytes, err = meter.Int64Histogram(durabilityBatchBytesMetricName, metric.WithUnit(byteUnit))
	if err != nil {
		failures = append(failures, err)
	}
	if len(failures) > 0 {
		return observerInstruments{}, fmt.Errorf("%w: %w", ErrInvalidObserverConfig, errors.Join(failures...))
	}
	return instruments, nil
}
