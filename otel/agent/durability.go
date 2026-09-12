package agent

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/attribute"

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
