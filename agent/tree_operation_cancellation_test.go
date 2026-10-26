package agent

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
)

func TestCanceledQueuedTreeOperationsLeaveTheCurrentOwnerAndHeadIntact(t *testing.T) {
	for _, operation := range []string{"capture", "release"} {
		t.Run(operation, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				engine := controlValue(NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter()}))
				defer mustCloseEngine(t, engine)
				result := controlValue(engine.Run(t.Context(), newChildTestDeployment(t), controlValue(EncodePayload(childTestInput{Mode: "leaf"}))))
				before := controlValue(engine.CaptureTree(t.Context(), result.ProcessID()))
				owner := controlValue(engine.acquireTreeOperation(t.Context(), result.ProcessID()))
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				finished := make(chan error, 1)
				go func() {
					if operation == "release" {
						finished <- engine.ReleaseTree(ctx, result.ProcessID())
						return
					}
					_, err := engine.CaptureTree(ctx, result.ProcessID())
					finished <- err
				}()
				synctest.Wait()
				select {
				case err := <-finished:
					t.Fatalf("tree operation bypassed its current owner: %v", err)
				default:
				}
				cancel()
				if err := <-finished; !errors.Is(err, context.Canceled) {
					t.Fatalf("queued operation ignored caller cancellation: %v", err)
				}
				if engine.treeOperations[result.ProcessID()] != owner {
					t.Fatal("canceling a waiter replaced the active tree owner")
				}
				owner.release()
				after := controlValue(engine.CaptureTree(t.Context(), result.ProcessID()))
				if before.Digest() != after.Digest() {
					t.Fatal("canceled operation changed the acknowledged tree head")
				}
			})
		})
	}
}
