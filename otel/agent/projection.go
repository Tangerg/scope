package agent

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/Tangerg/scope/agent/run"
)

const (
	processIDAttribute       attribute.Key = "agent.process.id"
	processRootIDAttribute   attribute.Key = "agent.process.root_id"
	processParentIDAttribute attribute.Key = "agent.process.parent_id"
	processDepthAttribute    attribute.Key = "agent.process.depth"
	processStatusAttribute   attribute.Key = "agent.process.status"
	processCauseAttribute    attribute.Key = "agent.process.cause"
	programDigestAttribute   attribute.Key = "agent.program.digest"
	epochAttribute           attribute.Key = "agent.epoch"
	lsnAttribute             attribute.Key = "agent.log.lsn"
	effectIDAttribute        attribute.Key = "agent.effect.id"
	effectTargetAttribute    attribute.Key = "agent.effect.target"
	effectStatusAttribute    attribute.Key = "agent.effect.status"
	incompleteClassification               = "agent.observation.incomplete"
)

type observedSpan struct {
	span       trace.Span
	at         time.Time
	attributes []attribute.KeyValue
}

type logProjection struct {
	observer  *Observer
	root      run.ProcessID
	processes map[run.ProcessID]observedSpan
	effects   map[run.EffectID]observedSpan
	done      chan struct{}
}

func newLogProjection(observer *Observer, root run.ProcessID) *logProjection {
	return &logProjection{observer: observer, root: root, processes: make(map[run.ProcessID]observedSpan), effects: make(map[run.EffectID]observedSpan), done: make(chan struct{})}
}

func (l *logProjection) apply(ctx context.Context, entry run.Entry) error {
	switch fact := entry.Record.(type) {
	case run.Started:
		l.startProcess(ctx, entry, fact)
	case run.Stepped, run.SignalAdmitted:
	case run.EffectIntent:
		l.startEffect(ctx, entry, fact)
	case run.EffectSettled:
		l.settleEffect(ctx, entry, fact)
	case run.Exited:
		l.exitProcess(ctx, entry, fact)
	case run.ExecutionFailed:
		if process, present := l.processes[fact.Process]; present {
			process.span.AddEvent("agent.execution.failed", trace.WithTimestamp(fact.At), trace.WithAttributes(semconv.ErrorTypeKey.String(fact.Failure.Code())))
		}
	case run.Activated, run.Reaped, run.Checkpoint, run.ControlRequested:
	default:
		return run.ErrInvalidLog
	}
	l.recordUsage(ctx, entry.Record)
	return nil
}

func (l *logProjection) recordUsage(ctx context.Context, record run.Record) {
	_, usage := run.UsageFromRecord(record)
	if usage.CommittedSteps != 0 {
		l.observer.instruments.steps.Add(ctx, int64(usage.CommittedSteps))
	}
	if usage.PreparedEffects != 0 {
		var options []metric.AddOption
		if intent, ok := record.(run.EffectIntent); ok {
			options = append(options, metric.WithAttributes(effectTargetAttribute.String(intent.Effect.Target().String())))
		}
		l.observer.instruments.effects.Add(ctx, int64(usage.PreparedEffects), options...)
	}
	if usage.AcceptedMessages != 0 {
		l.observer.instruments.messages.Add(ctx, int64(usage.AcceptedMessages))
	}
}

func (l *logProjection) startProcess(ctx context.Context, entry run.Entry, fact run.Started) {
	attributes := []attribute.KeyValue{semconv.GenAIOperationNameInvokeAgent, semconv.GenAIAgentName(fact.Program.Name())}
	parentID, hasParent := fact.Process.Parent()
	if parent, ok := l.processes[parentID]; ok {
		ctx = trace.ContextWithSpan(ctx, parent.span)
	}
	spanAttributes := append([]attribute.KeyValue{}, attributes...)
	spanAttributes = append(spanAttributes, processIDAttribute.String(fact.Process.String()), processRootIDAttribute.String(l.root.String()), processDepthAttribute.Int(int(fact.Process.Depth())), programDigestAttribute.String(fact.Program.Digest().String()), uint64Attribute(epochAttribute, uint64(entry.Epoch)), uint64Attribute(lsnAttribute, uint64(entry.LSN)))
	if hasParent {
		spanAttributes = append(spanAttributes, processParentIDAttribute.String(parentID.String()))
	}
	_, span := l.observer.tracer.Start(ctx, "invoke_agent "+fact.Program.Name(), trace.WithTimestamp(fact.At), trace.WithAttributes(spanAttributes...))
	l.processes[fact.Process] = observedSpan{span: span, at: fact.At, attributes: attributes}
	l.observer.instruments.processStarts.Add(ctx, 1, metric.WithAttributes(attributes...))
}

func (l *logProjection) startEffect(ctx context.Context, entry run.Entry, fact run.EffectIntent) {
	if process, ok := l.processes[fact.ID.Process]; ok {
		ctx = trace.ContextWithSpan(ctx, process.span)
	}
	attributes := []attribute.KeyValue{effectTargetAttribute.String(fact.Effect.Target().String())}
	spanAttributes := append([]attribute.KeyValue{}, attributes...)
	spanAttributes = append(spanAttributes, effectIDAttribute.String(fact.ID.String()), processIDAttribute.String(fact.ID.Process.String()), uint64Attribute(epochAttribute, uint64(entry.Epoch)), uint64Attribute(lsnAttribute, uint64(entry.LSN)))
	_, span := l.observer.tracer.Start(ctx, "agent.effect", trace.WithTimestamp(fact.At), trace.WithAttributes(spanAttributes...))
	l.effects[fact.ID] = observedSpan{span: span, at: fact.At, attributes: attributes}
}

func (l *logProjection) settleEffect(ctx context.Context, entry run.Entry, fact run.EffectSettled) {
	observed, ok := l.effects[fact.ID]
	if !ok {
		if process, present := l.processes[fact.ID.Process]; present {
			process.span.AddEvent("agent.effect.settled", trace.WithTimestamp(fact.At), trace.WithAttributes(effectIDAttribute.String(fact.ID.String()), effectStatusAttribute.String(fact.Outcome.Status().String()), uint64Attribute(lsnAttribute, uint64(entry.LSN))))
		}
		return
	}
	if fact.Outcome.Status() == run.SettlementStatusUnknown {
		attributes := []attribute.KeyValue{uint64Attribute(lsnAttribute, uint64(entry.LSN))}
		if fact.Failure != nil {
			attributes = append(attributes, semconv.ErrorTypeKey.String(fact.Failure.Code()))
		}
		observed.span.AddEvent("agent.effect.unknown", trace.WithTimestamp(fact.At), trace.WithAttributes(attributes...))
		return
	}
	attributes := append(observed.attributes, effectStatusAttribute.String(fact.Outcome.Status().String()))
	observed.span.SetAttributes(attributes...)
	if fact.Outcome.Status() != run.SettlementStatusSucceeded {
		classification := "agent.effect." + fact.Outcome.Status().String()
		if failure, failed := fact.Outcome.Failure(); failed {
			classification = failure.Code()
		}
		classifySpan(observed.span, classification)
	}
	if !fact.At.Before(observed.at) {
		l.observer.instruments.effectDuration.Record(ctx, fact.At.Sub(observed.at).Seconds(), metric.WithAttributes(attributes...))
	}
	observed.span.End(trace.WithTimestamp(fact.At))
	delete(l.effects, fact.ID)
}

func (l *logProjection) exitProcess(ctx context.Context, entry run.Entry, fact run.Exited) {
	attributes := []attribute.KeyValue{processStatusAttribute.String(fact.Termination.Status().String()), processCauseAttribute.String(fact.Termination.Cause().String())}
	l.observer.instruments.processExits.Add(ctx, 1, metric.WithAttributes(attributes...))
	observed, ok := l.processes[fact.Process]
	if !ok {
		return
	}
	attributes = append(attributes, observed.attributes...)
	observed.span.SetAttributes(attributes...)
	observed.span.SetAttributes(uint64Attribute(lsnAttribute, uint64(entry.LSN)))
	if fact.Termination.Status() != run.StatusCompleted {
		classification := "agent." + fact.Termination.Cause().String()
		if failure, failed := fact.Termination.Failure(); failed {
			classification = failure.Code()
		}
		classifySpan(observed.span, classification)
	}
	if !fact.At.Before(observed.at) {
		l.observer.instruments.processDuration.Record(ctx, fact.At.Sub(observed.at).Seconds(), metric.WithAttributes(attributes...))
	}
	observed.span.End(trace.WithTimestamp(fact.At))
	delete(l.processes, fact.Process)
}

func (l *logProjection) close() {
	for _, observed := range l.effects {
		classifySpan(observed.span, incompleteClassification)
		observed.span.End()
	}
	for _, observed := range l.processes {
		classifySpan(observed.span, incompleteClassification)
		observed.span.End()
	}
}

func classifySpan(span trace.Span, classification string) {
	span.SetStatus(codes.Error, classification)
	span.SetAttributes(semconv.ErrorTypeKey.String(classification))
	span.RecordError(fmt.Errorf("%s", classification))
}

func uint64Attribute(key attribute.Key, value uint64) attribute.KeyValue {
	if value > math.MaxInt64 {
		return key.String(strconv.FormatUint(value, 10))
	}
	return key.Int64(int64(value))
}
