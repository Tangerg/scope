package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/samber/lo"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	agent "github.com/Tangerg/scope/agent"
)

const (
	durabilityDurationMetricName                    = "agent.durability.duration"
	durabilitySnapshotBytesMetricName               = "agent.durability.snapshot.size"
	durabilityOperationAttribute      attribute.Key = "agent.durability.operation"
	durabilityBoundaryAttribute       attribute.Key = "agent.durability.boundary"
	durabilityOutcomeAttribute        attribute.Key = "agent.durability.outcome"
	durabilityHeadAttribute           attribute.Key = "agent.tree.head_digest"

	durabilityActivateOperation   = "activate"
	durabilityEffectOperation     = "effect"
	durabilityCheckpointOperation = "checkpoint"
	durabilityAcknowledged        = "acknowledged"
	durabilityOwnershipConflict   = "ownership_conflict"
	durabilityContentConflict     = "content_conflict"
	durabilityUnresolved          = "unresolved"
)

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

type observedTreeDurability struct {
	observer *Observer
	next     agent.TreeDurability
}

func (o *observedTreeDurability) ActivateTree(ctx context.Context, activation agent.TreeActivation) error {
	return o.observer.observeDurability(ctx, durabilityActivateOperation, "", activation.TreeSnapshot(), func(ctx context.Context) error {
		return o.next.ActivateTree(ctx, activation)
	})
}

func (o *observedTreeDurability) CommitEffect(ctx context.Context, boundary agent.EffectBoundary) error {
	return o.observer.observeDurability(ctx, durabilityEffectOperation, boundary.Kind().String(), boundary.TreeSnapshot(), func(ctx context.Context) error {
		return o.next.CommitEffect(ctx, boundary)
	})
}

func (o *observedTreeDurability) CommitCheckpoint(ctx context.Context, checkpoint agent.TreeCheckpoint) error {
	return o.observer.observeDurability(ctx, durabilityCheckpointOperation, checkpoint.Kind().String(), checkpoint.TreeSnapshot(), func(ctx context.Context) error {
		return o.next.CommitCheckpoint(ctx, checkpoint)
	})
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

func durabilityOutcome(err error) string {
	switch {
	case err == nil:
		return durabilityAcknowledged
	case errors.Is(err, agent.ErrTreeIncarnationConflict):
		return durabilityOwnershipConflict
	case errors.Is(err, agent.ErrDurabilityConflict):
		return durabilityContentConflict
	default:
		return durabilityUnresolved
	}
}

type durabilityFactError struct{ outcome string }

func (d durabilityFactError) Error() string     { return "agent durability " + d.outcome }
func (d durabilityFactError) ErrorType() string { return "agent.durability." + d.outcome }

var _ agent.TreeDurability = (*observedTreeDurability)(nil)
