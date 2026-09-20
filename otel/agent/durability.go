package agent

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/attribute"

	agent "github.com/Tangerg/scope/agent"
)

const (
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

type observedTreeCommitter struct {
	observer *Observer
	next     agent.TreeCommitter
}

func (o *observedTreeCommitter) ActivateTree(ctx context.Context, activation agent.TreeActivation) error {
	return o.observer.observeDurability(ctx, durabilityActivateOperation, "", activation.TreeSnapshot(), func(ctx context.Context) error {
		return o.next.ActivateTree(ctx, activation)
	})
}

func (o *observedTreeCommitter) CommitEffect(ctx context.Context, boundary agent.EffectBoundary) error {
	return o.observer.observeDurability(ctx, durabilityEffectOperation, boundary.Kind().String(), boundary.TreeSnapshot(), func(ctx context.Context) error {
		return o.next.CommitEffect(ctx, boundary)
	})
}

func (o *observedTreeCommitter) CommitCheckpoint(ctx context.Context, checkpoint agent.TreeCheckpoint) error {
	return o.observer.observeDurability(ctx, durabilityCheckpointOperation, checkpoint.Kind().String(), checkpoint.TreeSnapshot(), func(ctx context.Context) error {
		return o.next.CommitCheckpoint(ctx, checkpoint)
	})
}

func durabilityOutcome(err error) string {
	switch {
	case err == nil:
		return durabilityAcknowledged
	case errors.Is(err, agent.ErrTreeIncarnationConflict):
		return durabilityOwnershipConflict
	case errors.Is(err, agent.ErrCommitConflict):
		return durabilityContentConflict
	default:
		return durabilityUnresolved
	}
}

type durabilityFactError struct{ outcome string }

func (d durabilityFactError) Error() string     { return "agent committer " + d.outcome }
func (d durabilityFactError) ErrorType() string { return "agent.committer." + d.outcome }

var _ agent.TreeCommitter = (*observedTreeCommitter)(nil)
