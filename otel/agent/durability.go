package agent

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"iter"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/otel/errortelemetry"
)

const (
	logAppendOperation                      = "append"
	logLoadOperation                        = "load"
	logReadOperation                        = "read"
	logOperationAttribute     attribute.Key = "agent.log.operation"
	logOutcomeAttribute       attribute.Key = "agent.log.outcome"
	logAcknowledgedOutcome                  = "acknowledged"
	logConflictOutcome                      = "conflict"
	logUnresolvedOutcome                    = "unresolved"
	logConflictClassification               = "agent.log.conflict"
)

type observedStore struct {
	observer *Observer
	next     run.Store
}

func (o *observedStore) Append(ctx context.Context, batch run.Batch) (err error) {
	if !o.observer.enabled() {
		return o.next.Append(ctx, batch)
	}
	ctx, span := o.observer.tracer.Start(ctx, "agent.log.append", trace.WithAttributes(processRootIDAttribute.String(batch.Root.String()), uint64Attribute(epochAttribute, uint64(batch.Epoch)), uint64Attribute(lsnAttribute, uint64(batch.After))))
	start := time.Now()
	defer errortelemetry.Finish(&err, func(observed error) {
		o.record(ctx, span, logAppendOperation, start, observed)
	})
	err = o.next.Append(ctx, batch)
	if encoded, encodeErr := jsonv2.Marshal(batch); encodeErr == nil {
		o.observer.instruments.durabilityBytes.Record(ctx, int64(len(encoded)))
	}
	return err
}

func (o *observedStore) Load(ctx context.Context, root run.ProcessID) (log run.Log, found bool, err error) {
	if !o.observer.enabled() {
		return o.next.Load(ctx, root)
	}
	ctx, span := o.observer.tracer.Start(ctx, "agent.log.load", trace.WithAttributes(processRootIDAttribute.String(root.String())))
	start := time.Now()
	defer errortelemetry.Finish(&err, func(observed error) {
		o.record(ctx, span, logLoadOperation, start, observed)
	})
	return o.next.Load(ctx, root)
}

// Read is one span from the first pull to the end of iteration, because the
// iterator owns the storage operation until the caller stops consuming it.
func (o *observedStore) Read(ctx context.Context, root run.ProcessID, after run.LSN) iter.Seq2[run.Entry, error] {
	if !o.observer.enabled() {
		return o.next.Read(ctx, root, after)
	}
	return func(yield func(run.Entry, error) bool) {
		readCtx, span := o.observer.tracer.Start(ctx, "agent.log.read", trace.WithAttributes(processRootIDAttribute.String(root.String()), uint64Attribute(lsnAttribute, uint64(after))))
		start := time.Now()
		var failure error
		defer errortelemetry.Finish(&failure, func(observed error) {
			o.record(readCtx, span, logReadOperation, start, observed)
		})
		for entry, err := range o.next.Read(readCtx, root, after) {
			failure = err
			if !yield(entry, err) || err != nil {
				break
			}
		}
	}
}

func (o *observedStore) record(ctx context.Context, span trace.Span, operation string, start time.Time, err error) {
	defer span.End()
	outcome := logAcknowledgedOutcome
	if err != nil {
		outcome = logUnresolvedOutcome
		if errors.Is(err, run.ErrCommitConflict) || errors.Is(err, run.ErrEpochConflict) {
			outcome = logConflictOutcome
		}
		errortelemetry.Record(span, errortelemetry.Classify(err,
			errortelemetry.Class{Err: run.ErrCommitConflict, Type: logConflictClassification},
			errortelemetry.Class{Err: run.ErrEpochConflict, Type: logConflictClassification},
		))
	}
	attributes := []attribute.KeyValue{logOperationAttribute.String(operation), logOutcomeAttribute.String(outcome)}
	span.SetAttributes(attributes...)
	o.observer.instruments.durabilityDuration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attributes...))
}

var _ run.Store = (*observedStore)(nil)
