package agent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/samber/lo"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"

	agent "github.com/Tangerg/scope/agent"
)

const (
	instrumentationName = "github.com/Tangerg/scope/otel/agent"
	durationUnit        = "s"
	stepUnit            = "{step}"
	effectUnit          = "{effect}"
	signalUnit          = "{signal}"
	deltaUnit           = "{delta}"

	invokeAgentOperationName = "invoke_agent"
	stepSpanName             = "agent.step"
	effectSpanName           = "agent.effect"

	processActivationsMetricName     = "agent.process.activations"
	processExitsMetricName           = "agent.process.exits"
	invokeAgentDurationMetricName    = "gen_ai.invoke_agent.duration"
	processCommittedStepsMetricName  = "agent.process.committed_steps"
	processPreparedEffectsMetricName = "agent.process.prepared_effects"
	processAcceptedSignalsMetricName = "agent.process.accepted_signals"
	stepDurationMetricName           = "agent.step.duration"
	effectDurationMetricName         = "agent.effect.duration"
	deltaDropsMetricName             = "agent.delta.dropped"

	processIDAttribute            attribute.Key = "agent.process.id"
	processRootIDAttribute        attribute.Key = "agent.process.root_id"
	processParentIDAttribute      attribute.Key = "agent.process.parent_id"
	processDepthAttribute         attribute.Key = "agent.process.depth"
	processActivationAttribute    attribute.Key = "agent.process.activation"
	processStatusAttribute        attribute.Key = "agent.process.status"
	processCauseAttribute         attribute.Key = "agent.process.cause"
	processFailureKindAttribute   attribute.Key = "agent.failure.kind"
	processFailureCodeAttribute   attribute.Key = "agent.failure.code"
	processEventSequenceAttribute attribute.Key = "agent.process.event_sequence"
	stepSequenceAttribute         attribute.Key = "agent.step.sequence"
	stepStatusAttribute           attribute.Key = "agent.step.status"
	effectIDAttribute             attribute.Key = "agent.effect.id"
	effectTargetAttribute         attribute.Key = "agent.effect.target"
	effectStatusAttribute         attribute.Key = "agent.effect.status"
	eventPhaseAttribute           attribute.Key = "agent.event.phase"
	deploymentNameAttribute       attribute.Key = "agent.deployment.name"
	deploymentDigestAttribute     attribute.Key = "agent.deployment.digest"
	treeIncarnationIDAttribute    attribute.Key = "agent.tree.incarnation_id"
)

type processActivation string

const (
	processActivationStarted  processActivation = "started"
	processActivationRestored processActivation = "restored"
)

var (
	ErrInvalidObserverConfig = errors.New("agent otel: invalid observer configuration")
	errNilContext            = errors.New("agent otel: nil Context")
	errIncompleteSpan        = errors.New("agent otel: observer closed before span completion")
)

// ObserverConfig selects the official OpenTelemetry providers used by Observer.
// A nil provider uses the corresponding OpenTelemetry global provider.
type ObserverConfig struct {
	// TracerProvider creates Process, Step, and Effect spans. Nil uses the
	// OpenTelemetry global provider.
	TracerProvider trace.TracerProvider

	// MeterProvider creates Process lifecycle and usage instruments, Step/Effect
	// duration histograms, durability duration and snapshot size, and the Delta
	// drop counter. Nil uses the OpenTelemetry global provider.
	MeterProvider metric.MeterProvider
}

// Observer projects immutable Framework Event facts into OpenTelemetry spans
// and metrics. It implements agent.EventListener and is safe for concurrent
// calls. Observer never receives Process behavior or application state.
// Observer values must be constructed with NewObserver and must not be copied
// after first use.
type Observer struct {
	tracer trace.Tracer

	instruments observerInstruments

	lifecycleMu sync.Mutex
	inFlight    sync.WaitGroup
	closed      bool
	closeDone   chan struct{}
	stateMu     sync.Mutex
	processes   map[processKey]processSpanRecord
	steps       map[stepKey]trace.Span
	effects     map[effectKey]trace.Span
}

type processSpanRecord struct {
	span       trace.Span
	startedAt  time.Time
	activation processActivation
}

type processKey struct {
	processID     agent.ProcessID
	incarnationID agent.TreeIncarnationID
}

type stepKey struct {
	process  processKey
	sequence uint64
}

type effectKey struct {
	process  processKey
	effectID agent.EffectID
}

func processKeyFor(event agent.Event) processKey {
	incarnationID, _ := event.TreeIncarnationID()
	return processKey{processID: event.ProcessID(), incarnationID: incarnationID}
}

type observerInstruments struct {
	processActivations        metric.Int64Counter
	processExits              metric.Int64Counter
	processActivationDuration metric.Float64Histogram
	processCommittedSteps     metric.Int64Histogram
	processPreparedEffects    metric.Int64Histogram
	processAcceptedSignals    metric.Int64Histogram
	stepDuration              metric.Float64Histogram
	effectDuration            metric.Float64Histogram
	deltaDrops                metric.Int64Counter
	durabilityDuration        metric.Float64Histogram
	durabilitySnapshotBytes   metric.Int64Histogram
}

// NewObserver validates providers and creates the observation instruments.
// Instrument construction failures wrap ErrInvalidObserverConfig. Export
// failures remain with the configured providers and never change Agent state.
func NewObserver(config ObserverConfig) (*Observer, error) {
	if config.TracerProvider != nil && lo.IsNil(config.TracerProvider) {
		return nil, fmt.Errorf("%w: tracer provider is typed nil", ErrInvalidObserverConfig)
	}
	if config.MeterProvider != nil && lo.IsNil(config.MeterProvider) {
		return nil, fmt.Errorf("%w: meter provider is typed nil", ErrInvalidObserverConfig)
	}
	tracerProvider := config.TracerProvider
	if tracerProvider == nil {
		tracerProvider = otel.GetTracerProvider()
	}
	meterProvider := config.MeterProvider
	if meterProvider == nil {
		meterProvider = otel.GetMeterProvider()
	}
	instruments, err := newObserverInstruments(meterProvider.Meter(instrumentationName))
	if err != nil {
		return nil, err
	}
	return &Observer{
		tracer:      tracerProvider.Tracer(instrumentationName),
		instruments: instruments,
		processes:   make(map[processKey]processSpanRecord),
		steps:       make(map[stepKey]trace.Span),
		effects:     make(map[effectKey]trace.Span),
		closeDone:   make(chan struct{}),
	}, nil
}

func newObserverInstruments(meter metric.Meter) (observerInstruments, error) {
	processActivations, err := meter.Int64Counter(
		processActivationsMetricName,
		metric.WithDescription("Agent Process runtime activations started or restored."),
	)
	if err != nil {
		return observerInstruments{}, fmt.Errorf("%w: create process activations counter: %w", ErrInvalidObserverConfig, err)
	}
	processExits, err := meter.Int64Counter(
		processExitsMetricName,
		metric.WithDescription("Agent Process terminal outcomes."),
	)
	if err != nil {
		return observerInstruments{}, fmt.Errorf("%w: create process exits counter: %w", ErrInvalidObserverConfig, err)
	}
	processActivationDuration, err := meter.Float64Histogram(
		invokeAgentDurationMetricName,
		metric.WithDescription("Duration of one in-process agent invocation from start or restore to termination."),
		metric.WithUnit(durationUnit),
		metric.WithExplicitBucketBoundaries(0.1, 0.2, 0.4, 0.8, 1.6, 3.2, 6.4, 12.8, 25.6, 51.2, 102.4, 204.8, 409.6),
	)
	if err != nil {
		return observerInstruments{}, fmt.Errorf("%w: create process activation duration histogram: %w", ErrInvalidObserverConfig, err)
	}
	processCommittedSteps, err := meter.Int64Histogram(
		processCommittedStepsMetricName,
		metric.WithDescription("Committed Steps in one terminal Agent Process."),
		metric.WithUnit(stepUnit),
	)
	if err != nil {
		return observerInstruments{}, fmt.Errorf("%w: create process committed steps histogram: %w", ErrInvalidObserverConfig, err)
	}
	processPreparedEffects, err := meter.Int64Histogram(
		processPreparedEffectsMetricName,
		metric.WithDescription("Prepared Effects in one terminal Agent Process."),
		metric.WithUnit(effectUnit),
	)
	if err != nil {
		return observerInstruments{}, fmt.Errorf("%w: create process prepared effects histogram: %w", ErrInvalidObserverConfig, err)
	}
	processAcceptedSignals, err := meter.Int64Histogram(
		processAcceptedSignalsMetricName,
		metric.WithDescription("Accepted Signals in one terminal Agent Process."),
		metric.WithUnit(signalUnit),
	)
	if err != nil {
		return observerInstruments{}, fmt.Errorf("%w: create process accepted signals histogram: %w", ErrInvalidObserverConfig, err)
	}
	stepDuration, err := meter.Float64Histogram(
		stepDurationMetricName,
		metric.WithDescription("Execution Step wall-clock duration."),
		metric.WithUnit(durationUnit),
	)
	if err != nil {
		return observerInstruments{}, fmt.Errorf("%w: create step duration histogram: %w", ErrInvalidObserverConfig, err)
	}
	effectDuration, err := meter.Float64Histogram(
		effectDurationMetricName,
		metric.WithDescription("Framework or Dispatcher Effect attempt duration."),
		metric.WithUnit(durationUnit),
	)
	if err != nil {
		return observerInstruments{}, fmt.Errorf("%w: create effect duration histogram: %w", ErrInvalidObserverConfig, err)
	}
	deltaDrops, err := meter.Int64Counter(
		deltaDropsMetricName,
		metric.WithDescription("Best-effort Delta increments dropped before observation."),
		metric.WithUnit(deltaUnit),
	)
	if err != nil {
		return observerInstruments{}, fmt.Errorf("%w: create delta drop counter: %w", ErrInvalidObserverConfig, err)
	}
	durabilityDuration, err := meter.Float64Histogram(
		durabilityDurationMetricName,
		metric.WithDescription("Duration of one durable protocol acknowledgment attempt."),
		metric.WithUnit(durationUnit),
	)
	if err != nil {
		return observerInstruments{}, fmt.Errorf("%w: create durability duration histogram: %w", ErrInvalidObserverConfig, err)
	}
	durabilitySnapshotBytes, err := meter.Int64Histogram(
		durabilitySnapshotBytesMetricName,
		metric.WithDescription("Whole-tree snapshot bytes proposed at a durable protocol boundary."),
		metric.WithUnit("By"),
	)
	if err != nil {
		return observerInstruments{}, fmt.Errorf("%w: create durability snapshot size histogram: %w", ErrInvalidObserverConfig, err)
	}
	return observerInstruments{
		processActivations:        processActivations,
		processExits:              processExits,
		processActivationDuration: processActivationDuration,
		processCommittedSteps:     processCommittedSteps,
		processPreparedEffects:    processPreparedEffects,
		processAcceptedSignals:    processAcceptedSignals,
		stepDuration:              stepDuration,
		effectDuration:            effectDuration,
		deltaDrops:                deltaDrops,
		durabilityDuration:        durabilityDuration,
		durabilitySnapshotBytes:   durabilitySnapshotBytes,
	}, nil
}

func (o *Observer) OnEvent(ctx context.Context, event agent.Event) {
	if o == nil || !event.Valid() {
		return
	}
	if !o.beginObservation() {
		return
	}
	defer o.inFlight.Done()
	if ctx == nil {
		panic(errNilContext)
	}
	switch event.Name() {
	case agent.EventProcessStarted, agent.EventProcessRestored:
		o.startProcess(ctx, event)
	case agent.EventProcessFinished:
		o.finishProcess(ctx, event)
	case agent.EventRuntimeStopped:
		o.stopRuntime(ctx, event)
	case agent.EventStepStarted:
		o.startStep(ctx, event)
	case agent.EventStepFinished:
		o.finishStep(ctx, event)
	case agent.EventEffectStarted:
		o.startEffect(ctx, event)
	case agent.EventEffectFinished:
		o.finishEffect(ctx, event)
	case agent.EventDeltaDropped:
		o.recordDeltaDrop(ctx, event)
	default:
		o.addProcessEvent(event)
	}
}

func (o *Observer) beginObservation() bool {
	o.lifecycleMu.Lock()
	defer o.lifecycleMu.Unlock()
	if o.closed {
		return false
	}
	o.inFlight.Add(1)
	return true
}

// Close prevents new observation, waits for callbacks already in flight, and
// then ends any incomplete spans. It is safe to call concurrently and is
// idempotent; normally Engine.Close leaves no incomplete Process spans.
func (o *Observer) Close() {
	if o == nil {
		return
	}
	o.lifecycleMu.Lock()
	if o.closed {
		done := o.closeDone
		o.lifecycleMu.Unlock()
		<-done
		return
	}
	o.closed = true
	o.lifecycleMu.Unlock()
	o.inFlight.Wait()
	defer close(o.closeDone)
	o.stateMu.Lock()
	spans := make([]trace.Span, 0, len(o.effects)+len(o.steps)+len(o.processes))
	for _, record := range o.effects {
		spans = append(spans, record)
	}
	for _, record := range o.steps {
		spans = append(spans, record)
	}
	for _, record := range o.processes {
		spans = append(spans, record.span)
	}
	clear(o.effects)
	clear(o.steps)
	clear(o.processes)
	o.stateMu.Unlock()
	endedAt := time.Now()
	for _, span := range spans {
		recordSpanFailure(span, errIncompleteSpan, endedAt)
		span.End(trace.WithTimestamp(endedAt))
	}
}

func (o *Observer) startProcess(ctx context.Context, event agent.Event) {
	relation := event.Relation()
	parentContext := ctx
	if parentID, child := relation.ParentID(); child {
		o.stateMu.Lock()
		parent, found := o.processes[processKey{processID: parentID, incarnationID: processKeyFor(event).incarnationID}]
		o.stateMu.Unlock()
		if found {
			parentContext = trace.ContextWithSpan(ctx, parent.span)
		}
	}
	activation := activationForEvent(event)
	spanAttributes := append(
		processAttributes(event),
		semconv.GenAIOperationNameInvokeAgent,
		semconv.GenAIAgentName(event.DeploymentRef().Name()),
		processActivationAttribute.String(string(activation)),
	)
	_, span := o.tracer.Start(
		parentContext, invokeAgentOperationName+" "+event.DeploymentRef().Name(),
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithTimestamp(event.OccurredAt()),
		trace.WithAttributes(spanAttributes...),
	)
	record := processSpanRecord{
		span: span, startedAt: event.OccurredAt(), activation: activation,
	}
	o.stateMu.Lock()
	if _, exists := o.processes[processKeyFor(event)]; exists {
		o.stateMu.Unlock()
		span.End(trace.WithTimestamp(event.OccurredAt()))
		return
	}
	o.processes[processKeyFor(event)] = record
	o.stateMu.Unlock()
	attributes := append(
		deploymentMetricAttributes(event),
		processActivationAttribute.String(string(activation)),
	)
	o.instruments.processActivations.Add(ctx, 1, metric.WithAttributes(attributes...))
}

func (o *Observer) finishProcess(ctx context.Context, event agent.Event) {
	fact, ok := event.ProcessFinished()
	if !ok {
		return
	}
	o.stateMu.Lock()
	record, found := o.processes[processKeyFor(event)]
	if found {
		delete(o.processes, processKeyFor(event))
	}
	o.stateMu.Unlock()
	attributes := append(deploymentMetricAttributes(event),
		processStatusAttribute.String(fact.Status().String()),
		processCauseAttribute.String(fact.Cause().String()),
	)
	if found {
		attributes = append(
			attributes,
			processActivationAttribute.String(string(record.activation)),
		)
	}
	if failureKind, failureCode, failed := fact.Failure(); failed {
		attributes = append(attributes,
			processFailureKindAttribute.String(failureKind.String()),
			processFailureCodeAttribute.String(failureCode),
		)
	}
	metricOptions := metric.WithAttributes(attributes...)
	o.instruments.processExits.Add(ctx, 1, metricOptions)
	usage := fact.Usage()
	o.instruments.processCommittedSteps.Record(ctx, saturatingInt64(usage.CommittedSteps), metricOptions)
	o.instruments.processPreparedEffects.Record(ctx, saturatingInt64(usage.PreparedEffects), metricOptions)
	o.instruments.processAcceptedSignals.Record(ctx, saturatingInt64(usage.AcceptedSignals), metricOptions)
	if !found {
		return
	}
	durationAttributes := []attribute.KeyValue{
		semconv.GenAIAgentName(event.DeploymentRef().Name()),
		processActivationAttribute.String(string(record.activation)),
	}
	observedError := processFactError{status: fact.Status(), cause: fact.Cause()}
	if failureKind, failureCode, failed := fact.Failure(); failed {
		observedError.failureKind = failureKind
		observedError.failureCode = failureCode
	}
	if processStatusIsError(fact.Status()) {
		durationAttributes = append(durationAttributes, semconv.ErrorType(observedError))
	}
	o.instruments.processActivationDuration.Record(
		trace.ContextWithSpan(ctx, record.span), elapsedSeconds(record.startedAt, event.OccurredAt()),
		metric.WithAttributes(durationAttributes...),
	)
	spanAttributes := []attribute.KeyValue{
		processStatusAttribute.String(fact.Status().String()),
		processCauseAttribute.String(fact.Cause().String()),
	}
	if failureKind, failureCode, failed := fact.Failure(); failed {
		spanAttributes = append(spanAttributes,
			processFailureKindAttribute.String(failureKind.String()),
			processFailureCodeAttribute.String(failureCode),
		)
	}
	record.span.SetAttributes(spanAttributes...)
	if processStatusIsError(fact.Status()) {
		recordSpanFailure(record.span, observedError, event.OccurredAt())
	}
	record.span.End(trace.WithTimestamp(event.OccurredAt()))
}

func (o *Observer) startStep(ctx context.Context, event agent.Event) {
	sequence, ok := event.StepSequence()
	if !ok {
		return
	}
	o.stateMu.Lock()
	process, found := o.processes[processKeyFor(event)]
	if !found {
		o.stateMu.Unlock()
		return
	}
	key := stepKey{process: processKeyFor(event), sequence: sequence}
	if _, exists := o.steps[key]; exists {
		o.stateMu.Unlock()
		return
	}
	o.stateMu.Unlock()
	attributes := append(
		processAttributes(event),
		processActivationAttribute.String(string(process.activation)),
		uint64Attribute(stepSequenceAttribute, sequence),
	)
	_, span := o.tracer.Start(
		trace.ContextWithSpan(ctx, process.span), stepSpanName,
		trace.WithTimestamp(event.OccurredAt()),
		trace.WithAttributes(attributes...),
	)
	o.stateMu.Lock()
	if _, exists := o.steps[key]; exists {
		o.stateMu.Unlock()
		span.End(trace.WithTimestamp(event.OccurredAt()))
		return
	}
	o.steps[key] = span
	o.stateMu.Unlock()
}

func (o *Observer) finishStep(ctx context.Context, event agent.Event) {
	sequence, ok := event.StepSequence()
	if !ok {
		return
	}
	key := stepKey{process: processKeyFor(event), sequence: sequence}
	fact, ok := event.StepFinished()
	if !ok {
		return
	}
	o.stateMu.Lock()
	record, found := o.steps[key]
	if found {
		delete(o.steps, key)
	}
	o.stateMu.Unlock()
	metricAttributes := append(
		deploymentMetricAttributes(event),
		stepStatusAttribute.String(fact.Status().String()),
	)
	o.instruments.stepDuration.Record(
		ctx, fact.Duration().Seconds(), metric.WithAttributes(metricAttributes...),
	)
	if !found {
		return
	}
	record.SetAttributes(stepStatusAttribute.String(fact.Status().String()))
	if fact.Status() == agent.StepStatusFailed {
		recordSpanFailure(record, stepFactError{}, event.OccurredAt())
	}
	record.End(trace.WithTimestamp(event.OccurredAt()))
}

func (o *Observer) startEffect(ctx context.Context, event agent.Event) {
	effectID, ok := event.EffectID()
	if !ok {
		return
	}
	fact, ok := event.EffectStarted()
	if !ok {
		return
	}
	o.stateMu.Lock()
	process, found := o.processes[processKeyFor(event)]
	if !found {
		o.stateMu.Unlock()
		return
	}
	if _, exists := o.effects[effectKey{process: processKeyFor(event), effectID: effectID}]; exists {
		o.stateMu.Unlock()
		return
	}
	o.stateMu.Unlock()
	attributes := append(
		processAttributes(event),
		processActivationAttribute.String(string(process.activation)),
		effectIDAttribute.String(effectID.String()),
		effectTargetAttribute.String(fact.Target().String()),
	)
	_, span := o.tracer.Start(
		trace.ContextWithSpan(ctx, process.span), effectSpanName,
		trace.WithTimestamp(event.OccurredAt()),
		trace.WithAttributes(attributes...),
	)
	o.stateMu.Lock()
	if _, exists := o.effects[effectKey{process: processKeyFor(event), effectID: effectID}]; exists {
		o.stateMu.Unlock()
		span.End(trace.WithTimestamp(event.OccurredAt()))
		return
	}
	o.effects[effectKey{process: processKeyFor(event), effectID: effectID}] = span
	o.stateMu.Unlock()
}

func (o *Observer) finishEffect(ctx context.Context, event agent.Event) {
	effectID, ok := event.EffectID()
	if !ok {
		return
	}
	fact, ok := event.EffectFinished()
	if !ok {
		return
	}
	o.stateMu.Lock()
	record, found := o.effects[effectKey{process: processKeyFor(event), effectID: effectID}]
	if found {
		delete(o.effects, effectKey{process: processKeyFor(event), effectID: effectID})
	}
	o.stateMu.Unlock()
	metricAttributes := append(
		deploymentMetricAttributes(event),
		effectTargetAttribute.String(fact.Target().String()),
		effectStatusAttribute.String(fact.SettlementStatus().String()),
	)
	o.instruments.effectDuration.Record(
		ctx, fact.Duration().Seconds(), metric.WithAttributes(metricAttributes...),
	)
	if !found {
		return
	}
	record.SetAttributes(
		effectTargetAttribute.String(fact.Target().String()),
		effectStatusAttribute.String(fact.SettlementStatus().String()),
	)
	if fact.SettlementStatus() != agent.SettlementStatusSucceeded {
		recordSpanFailure(record, effectFactError{
			target: fact.Target(), settlement: fact.SettlementStatus(),
		}, event.OccurredAt())
	}
	record.End(trace.WithTimestamp(event.OccurredAt()))
}

func (o *Observer) recordDeltaDrop(ctx context.Context, event agent.Event) {
	fact, ok := event.DeltaDropped()
	if ok {
		o.instruments.deltaDrops.Add(
			ctx, saturatingInt64(fact.Count()),
			metric.WithAttributes(deploymentMetricAttributes(event)...),
		)
	}
	o.addProcessEvent(event)
}

func (o *Observer) addProcessEvent(event agent.Event) {
	o.stateMu.Lock()
	record, found := o.processes[processKeyFor(event)]
	o.stateMu.Unlock()
	if !found {
		return
	}
	attributes := []attribute.KeyValue{
		uint64Attribute(processEventSequenceAttribute, event.ProcessSequence()),
		eventPhaseAttribute.String(event.Phase().String()),
	}
	if step, ok := event.StepSequence(); ok {
		attributes = append(attributes, uint64Attribute(stepSequenceAttribute, step))
	}
	if effectID, ok := event.EffectID(); ok {
		attributes = append(attributes, effectIDAttribute.String(effectID.String()))
	}
	record.span.AddEvent(
		event.Name(), trace.WithTimestamp(event.OccurredAt()), trace.WithAttributes(attributes...),
	)
}

// WrapDispatcher propagates the observed Effect span into downstream calls.
// Register this same Observer as an Engine EventListener: the Engine publishes
// EffectStarted before dispatch and EffectFinished after dispatch returns.
// Without an active observed Effect, the caller's context passes through.
// The returned decorator preserves replay policy, settlement, and Delta delivery.
func (o *Observer) WrapDispatcher(next agent.Dispatcher) (agent.Dispatcher, error) {
	if o == nil || lo.IsNil(o.tracer) {
		return nil, fmt.Errorf("%w: observer must be constructed with NewObserver", ErrInvalidObserverConfig)
	}
	if lo.IsNil(next) {
		return nil, fmt.Errorf("%w: dispatcher must not be nil", ErrInvalidObserverConfig)
	}
	return &observedDispatcher{observer: o, next: next}, nil
}

// WrapTreeDurability observes the existing port so instrumentation cannot select
// a different commit or fencing path. Metric labels stay bounded to avoid one
// time series per tree; identities belong only in traces. Adapter diagnostics
// are excluded because they can contain credentials or payloads. An error other
// than an explicit conflict remains unresolved because a lost response cannot
// prove whether storage committed.
func (o *Observer) WrapTreeDurability(next agent.TreeDurability) (agent.TreeDurability, error) {
	if o == nil || lo.IsNil(o.tracer) {
		return nil, fmt.Errorf("%w: observer must be constructed with NewObserver", ErrInvalidObserverConfig)
	}
	if lo.IsNil(next) {
		return nil, fmt.Errorf("%w: tree durability must not be nil", ErrInvalidObserverConfig)
	}
	return &observedTreeDurability{observer: o, next: next}, nil
}

func (o *Observer) observeDurability(ctx context.Context, operation, boundary string, snapshot agent.TreeSnapshot, invoke func(context.Context) error) (err error) {
	if !o.beginObservation() {
		return invoke(ctx)
	}
	defer o.inFlight.Done()
	if ctx == nil {
		panic(errNilContext)
	}
	attributes := []attribute.KeyValue{durabilityOperationAttribute.String(operation)}
	if boundary != "" {
		attributes = append(attributes, durabilityBoundaryAttribute.String(boundary))
	}
	ctx, span := o.tracer.Start(ctx, "agent.durability."+operation, trace.WithAttributes(attributes...))
	if snapshot.Valid() {
		span.SetAttributes(
			processRootIDAttribute.String(snapshot.RootID().String()),
			durabilityHeadAttribute.String(snapshot.Digest().String()),
		)
		if incarnationID, durable := snapshot.IncarnationID(); durable {
			span.SetAttributes(treeIncarnationIDAttribute.String(incarnationID.String()))
		}
	}
	startedAt := time.Now()
	defer func() {
		panicked := recover()
		outcome := durabilityOutcome(err)
		if panicked != nil {
			outcome = durabilityUnresolved
		}
		finishedAt := time.Now()
		attributes = append(attributes, durabilityOutcomeAttribute.String(outcome))
		span.SetAttributes(durabilityOutcomeAttribute.String(outcome))
		if outcome != durabilityAcknowledged {
			recordSpanFailure(span, durabilityFactError{outcome: outcome}, finishedAt)
		}
		options := metric.WithAttributes(attributes...)
		o.instruments.durabilityDuration.Record(ctx, finishedAt.Sub(startedAt).Seconds(), options)
		if snapshot.Valid() {
			o.instruments.durabilitySnapshotBytes.Record(ctx, int64(len(snapshot.JSON())), options)
		}
		span.End(trace.WithTimestamp(finishedAt))
		if panicked != nil {
			panic(panicked)
		}
	}()
	return invoke(ctx)
}

func (o *Observer) stopRuntime(ctx context.Context, event agent.Event) {
	fact, ok := event.RuntimeStopped()
	if !ok {
		return
	}
	key := processKeyFor(event)
	o.stateMu.Lock()
	record, found := o.processes[key]
	delete(o.processes, key)
	var spans []trace.Span
	for step, span := range o.steps {
		if step.process == key {
			spans = append(spans, span)
			delete(o.steps, step)
		}
	}
	for effect, span := range o.effects {
		if effect.process == key {
			spans = append(spans, span)
			delete(o.effects, effect)
		}
	}
	o.stateMu.Unlock()
	observedError := runtimeFactError{kind: fact.FailureKind(), code: fact.FailureCode()}
	if found {
		spans = append(spans, record.span)
		attributes := []attribute.KeyValue{
			semconv.GenAIAgentName(event.DeploymentRef().Name()),
			processActivationAttribute.String(string(record.activation)),
			semconv.ErrorType(observedError),
		}
		o.instruments.processActivationDuration.Record(
			trace.ContextWithSpan(ctx, record.span), elapsedSeconds(record.startedAt, event.OccurredAt()),
			metric.WithAttributes(attributes...),
		)
	}
	for _, span := range spans {
		span.SetAttributes(
			processFailureKindAttribute.String(fact.FailureKind().String()),
			processFailureCodeAttribute.String(fact.FailureCode()),
		)
		recordSpanFailure(span, observedError, event.OccurredAt())
		span.End(trace.WithTimestamp(event.OccurredAt()))
	}
}

func uint64Attribute(key attribute.Key, value uint64) attribute.KeyValue {
	return key.String(strconv.FormatUint(value, 10))
}

func saturatingInt64(value uint64) int64 {
	if value > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(value)
}

func processAttributes(event agent.Event) []attribute.KeyValue {
	reference := event.DeploymentRef()
	relation := event.Relation()
	values := []attribute.KeyValue{
		processIDAttribute.String(event.ProcessID().String()),
		processRootIDAttribute.String(relation.RootID().String()),
		processDepthAttribute.Int64(int64(relation.Depth())),
		deploymentNameAttribute.String(reference.Name()),
		deploymentDigestAttribute.String(reference.Digest().String()),
	}
	if parentID, child := relation.ParentID(); child {
		values = append(values, processParentIDAttribute.String(parentID.String()))
	}
	if incarnationID, durable := event.TreeIncarnationID(); durable {
		values = append(values, treeIncarnationIDAttribute.String(incarnationID.String()))
	}
	return values
}

func deploymentMetricAttributes(event agent.Event) []attribute.KeyValue {
	reference := event.DeploymentRef()
	return []attribute.KeyValue{
		deploymentNameAttribute.String(reference.Name()),
	}
}

func elapsedSeconds(startedAt, finishedAt time.Time) float64 {
	if finishedAt.Before(startedAt) {
		return 0
	}
	return finishedAt.Sub(startedAt).Seconds()
}

type processFactError struct {
	status      agent.Status
	cause       agent.TerminationCause
	failureKind agent.FailureKind
	failureCode string
}

func (p processFactError) Error() string {
	if p.failureCode != "" {
		return "agent Process " + p.status.String() + ": " +
			p.failureKind.String() + "/" + p.failureCode
	}
	return "agent Process " + p.status.String() + ": " + p.cause.String()
}

func (p processFactError) ErrorType() string {
	if p.failureCode != "" {
		return p.failureCode
	}
	return "agent." + p.cause.String()
}

type stepFactError struct{}

func (stepFactError) Error() string { return "agent Execution Step failed" }

func (stepFactError) ErrorType() string { return "agent.step.failed" }

type effectFactError struct {
	target     agent.EffectTarget
	settlement agent.SettlementStatus
}

func (e effectFactError) Error() string {
	return "agent " + e.target.String() + " Effect " + e.settlement.String()
}

func (e effectFactError) ErrorType() string { return "agent.effect." + e.settlement.String() }

func recordSpanFailure(span trace.Span, observedError error, occurredAt time.Time) {
	span.SetAttributes(semconv.ErrorType(observedError))
	span.RecordError(observedError, trace.WithTimestamp(occurredAt))
	span.SetStatus(codes.Error, observedError.Error())
}

func activationForEvent(event agent.Event) processActivation {
	if event.Name() == agent.EventProcessRestored {
		return processActivationRestored
	}
	return processActivationStarted
}

func processStatusIsError(status agent.Status) bool {
	switch status {
	case agent.StatusFailed, agent.StatusTimedOut, agent.StatusKilled:
		return true
	default:
		return false
	}
}

var _ agent.EventListener = (*Observer)(nil)
