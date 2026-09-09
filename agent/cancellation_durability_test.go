package agent

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
)

func TestCancellationPreservesSettlementAcknowledgment(t *testing.T) {
	for _, reject := range []bool{false, true} {
		name := "acknowledged"
		if reject {
			name = "failed"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				durability := &cancellationSettlementDurability{
					recordingTreeDurability: &recordingTreeDurability{},
					entered:                 make(chan context.Context, 1), release: make(chan struct{}),
				}
				if reject {
					durability.failure = errors.New("settlement acknowledgment unavailable")
				}
				releaseCommit := sync.OnceFunc(func() { close(durability.release) })
				defer releaseCommit()
				dispatcher := &cancellationDispatcher{
					entered: make(chan EffectRequest, 1), canceled: make(chan struct{}),
					release: make(chan struct{}), status: SettlementStatusSucceeded,
				}
				releaseDispatch := sync.OnceFunc(func() { close(dispatcher.release) })
				defer releaseDispatch()
				engine, err := NewEngine(EngineConfig{TreeDurability: durability})
				if err != nil {
					t.Fatal(err)
				}
				deployment := engineTestDeployment(t, newEngineTestDefinition(t, "engine.effect", "effect"), dispatcher)
				input, _ := EncodeInput(engineTestInput{Value: "settle before termination"})
				process, err := engine.Start(t.Context(), deployment, input)
				if err != nil {
					t.Fatal(err)
				}
				request := <-dispatcher.entered
				if err := process.Kill(t.Context(), "collect the actual result"); err != nil {
					t.Fatal(err)
				}
				<-dispatcher.canceled
				releaseDispatch()
				commitContext := <-durability.entered
				if commitContext.Done() != nil || commitContext.Err() != nil {
					t.Error("Process cancellation reached required settlement acknowledgment")
				}
				before := inspectProcessSnapshot(t, process)
				if before.Status().Terminal() {
					t.Error("unacknowledged settlement published a terminal Process")
				}
				releaseCommit()
				if reject {
					runtimeErr := awaitRuntimeError(t, process, durability.failure)
					unresolved := runtimeErr.UnresolvedEffectIDs()
					if len(unresolved) != 1 || unresolved[0] != request.ID() ||
						!bytes.Equal(inspectProcessSnapshot(t, process).JSON(), before.JSON()) {
						t.Errorf("failed acknowledgment changed authoritative facts: %+v", runtimeErr)
					}
				} else if result := mustAwait(t, process); result.Status() != StatusKilled || len(result.Termination().UnresolvedEffectIDs()) != 0 {
					t.Errorf("acknowledged termination = %+v", result.Termination())
				}
				mustCloseEngine(t, engine)
			})
		})
	}
}

type cancellationSettlementDurability struct {
	*recordingTreeDurability
	entered chan context.Context
	release chan struct{}
	failure error
}

func (c *cancellationSettlementDurability) CommitEffect(ctx context.Context, boundary EffectBoundary) error {
	if boundary.Kind() == EffectBoundarySettled {
		c.entered <- ctx
		<-c.release
		if c.failure != nil {
			return c.failure
		}
	}
	return c.recordingTreeDurability.CommitEffect(ctx, boundary)
}
