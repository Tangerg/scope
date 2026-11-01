package agent

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/attribute"

	"github.com/Tangerg/scope/agent/run"
)

const (
	durabilitySpanPrefix                            = "agent.committer."
	durabilityDurationMetricName                    = "agent.committer.duration"
	durabilitySnapshotBytesMetricName               = "agent.committer.snapshot.size"
	durabilityOperationAttribute      attribute.Key = "agent.committer.operation"
	durabilityBoundaryAttribute       attribute.Key = "agent.committer.boundary"
	durabilityOutcomeAttribute        attribute.Key = "agent.committer.outcome"
	durabilityHeadAttribute           attribute.Key = "agent.tree.head_digest"

	durabilityActivateOperation   = "activate"
	durabilityEffectOperation     = "effect"
	durabilityCheckpointOperation = "checkpoint"
	durabilityAcknowledged        = "acknowledged"
	durabilityOwnershipConflict   = "ownership_conflict"
	durabilityContentConflict     = "content_conflict"
	durabilityUnresolved          = "unresolved"
)

// Observation decorates writes while retaining exactly the original read authority.
type observedReadableStore struct {
	*observedTreeCommitter
	run.HeadReader
}

type observedTreeCommitter struct {
	observer *Observer
	next     run.Store
}

func (o *observedTreeCommitter) ActivateTree(ctx context.Context, activation run.TreeActivation) error {
	return o.observer.observeDurability(ctx, durabilityActivateOperation, "", activation.TreeSnapshot(), func(ctx context.Context) error {
		return o.next.ActivateTree(ctx, activation)
	})
}

func (o *observedTreeCommitter) CommitEffect(ctx context.Context, boundary run.EffectBoundary) error {
	return o.observer.observeDurability(ctx, durabilityEffectOperation, boundary.Kind().String(), boundary.TreeSnapshot(), func(ctx context.Context) error {
		return o.next.CommitEffect(ctx, boundary)
	})
}

func (o *observedTreeCommitter) CommitCheckpoint(ctx context.Context, checkpoint run.TreeCheckpoint) error {
	return o.observer.observeDurability(ctx, durabilityCheckpointOperation, checkpoint.Kind().String(), checkpoint.TreeSnapshot(), func(ctx context.Context) error {
		return o.next.CommitCheckpoint(ctx, checkpoint)
	})
}

func durabilityOutcome(err error) string {
	switch {
	case err == nil:
		return durabilityAcknowledged
	case errors.Is(err, run.ErrTreeIncarnationConflict):
		return durabilityOwnershipConflict
	case errors.Is(err, run.ErrCommitConflict):
		return durabilityContentConflict
	default:
		return durabilityUnresolved
	}
}

type durabilityError struct{ outcome string }

func (d durabilityError) Error() string     { return "agent committer " + d.outcome }
func (d durabilityError) ErrorType() string { return "agent.committer." + d.outcome }

var _ run.Store = (*observedTreeCommitter)(nil)

func (o *observedTreeCommitter) LookupStart(ctx context.Context, id run.StartID) (run.StartReceipt, bool, error) {
	return o.next.LookupStart(ctx, id)
}
