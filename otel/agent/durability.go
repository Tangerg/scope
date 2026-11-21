package agent

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"iter"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/otel/errortelemetry"
	"github.com/Tangerg/scope/otel/internal/instrument"
)

const (
	logSpanPrefix                           = "agent.log."
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

func logSpanName(operation string) string { return logSpanPrefix + operation }

type observedStore struct {
	middleware Middleware
	next       run.Store
}

func (o *observedStore) Append(ctx context.Context, batch run.Batch) (err error) {
	ctx, measurement := instrument.StartMeasurement(ctx, o.middleware.tracer, logSpanName(logAppendOperation), trace.SpanKindInternal, processRootIDAttribute.String(batch.Root.String()), uint64Attribute(epochAttribute, uint64(batch.Epoch)), uint64Attribute(lsnAttribute, uint64(batch.After)))
	defer errortelemetry.Finish(&err, func(observed error) {
		o.record(ctx, measurement, logAppendOperation, observed)
	})
	err = o.next.Append(ctx, batch)
	if encoded, encodeErr := jsonv2.Marshal(batch); encodeErr == nil {
		o.middleware.durabilityBytes.Record(ctx, int64(len(encoded)))
	}
	return err
}

func (o *observedStore) Load(ctx context.Context, root run.ProcessID) (log run.Log, found bool, err error) {
	ctx, measurement := instrument.StartMeasurement(ctx, o.middleware.tracer, logSpanName(logLoadOperation), trace.SpanKindInternal, processRootIDAttribute.String(root.String()))
	defer errortelemetry.Finish(&err, func(observed error) {
		o.record(ctx, measurement, logLoadOperation, observed)
	})
	return o.next.Load(ctx, root)
}

// Read is one span from the first pull to the end of iteration, because the
// iterator owns the storage operation until the caller stops consuming it.
func (o *observedStore) Read(ctx context.Context, root run.ProcessID, after run.LSN) iter.Seq2[run.Entry, error] {
	return func(yield func(run.Entry, error) bool) {
		readCtx, measurement := instrument.StartMeasurement(ctx, o.middleware.tracer, logSpanName(logReadOperation), trace.SpanKindInternal, processRootIDAttribute.String(root.String()), uint64Attribute(lsnAttribute, uint64(after)))
		var failure error
		defer errortelemetry.Finish(&failure, func(observed error) {
			o.record(readCtx, measurement, logReadOperation, observed)
		})
		for entry, err := range o.next.Read(readCtx, root, after) {
			failure = err
			if !yield(entry, err) || err != nil {
				break
			}
		}
	}
}

func (o *observedStore) record(ctx context.Context, measurement instrument.Measurement, operation string, err error) {
	outcome := logAcknowledgedOutcome
	if err != nil {
		outcome = logUnresolvedOutcome
		if errors.Is(err, run.ErrCommitConflict) || errors.Is(err, run.ErrEpochConflict) {
			outcome = logConflictOutcome
		}
	}
	attributes := []attribute.KeyValue{logOperationAttribute.String(operation), logOutcomeAttribute.String(outcome)}
	measurement.SetAttributes(attributes...)
	errorType := errortelemetry.Classify(err,
		errortelemetry.Class{Err: run.ErrCommitConflict, Type: logConflictClassification},
		errortelemetry.Class{Err: run.ErrEpochConflict, Type: logConflictClassification},
	)
	measurement.End(ctx, o.middleware.durabilityDuration, errorType, attributes...)
}

var _ run.Store = (*observedStore)(nil)
