package agent

import (
	"context"
	"errors"
	"testing"
)

func TestCaptureWaitsForAcknowledgmentAndRejectsFailedCut(t *testing.T) {
	for _, reject := range []bool{false, true} {
		name := "acknowledged"
		if reject {
			name = "rejected"
		}
		t.Run(name, func(t *testing.T) {
			runtime, process := newChildCompletionTestProcess(t)
			base := runtime.head
			store := runtime.engine.committer.(*MemoryTreeCommitter)
			gate := &captureCheckpointCommitter{MemoryTreeCommitter: store, entered: make(chan struct{}), release: make(chan struct{})}
			if reject {
				gate.err = errors.New("capture checkpoint rejected")
			}
			runtime.engine.committer = gate
			runtime.failProcessContract(process, "engine.capture.test", errors.New("candidate termination"))
			acquisition := &treeFreezeAcquisition{response: make(chan treeFreezeAcquisitionResult, 1), canceled: make(chan struct{})}
			runtime.acquireFreeze(acquisition)
			<-gate.entered
			select {
			case <-acquisition.response:
				t.Fatal("capture returned before acknowledgment")
			default:
			}
			select {
			case <-process.handle.outcomePublished:
				t.Fatal("candidate outcome published before acknowledgment")
			default:
			}
			inspection, inspectErr := runtime.buildInspection()
			if inspectErr != nil {
				t.Fatal(inspectErr)
			}
			if inspection.HeadDigest != base.Digest() {
				t.Fatal("inspection exposed the candidate cut")
			}
			close(gate.release)
			runtime.applyTreeCommitCompletion(<-runtime.commitDone)
			captured := <-acquisition.response
			head, exists, err := store.LoadTree(t.Context(), runtime.rootID)
			if err != nil || !exists {
				t.Fatalf("load head: exists=%t error=%v", exists, err)
			}
			if reject {
				if !errors.Is(captured.err, gate.err) || captured.snapshot.Valid() || head.Digest() != base.Digest() {
					t.Fatalf("failed capture changed recovery state: %+v", captured)
				}
				again := &treeFreezeAcquisition{response: make(chan treeFreezeAcquisitionResult, 1), canceled: make(chan struct{})}
				runtime.acquireFreeze(again)
				if retry := <-again.response; !errors.Is(retry.err, gate.err) || retry.snapshot.Valid() {
					t.Fatal("failed runtime manufactured a recovery cut")
				}
			} else {
				if captured.err != nil || !captured.snapshot.Valid() || captured.snapshot.Digest() != head.Digest() || head.Digest() == base.Digest() {
					t.Fatalf("capture did not return the acknowledged cut: %+v", captured)
				}
				if err := runtime.releaseFreeze(captured.freeze); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

type captureCheckpointCommitter struct {
	*MemoryTreeCommitter
	entered chan struct{}
	release chan struct{}
	err     error
}

func (c *captureCheckpointCommitter) CommitCheckpoint(ctx context.Context, checkpoint TreeCheckpoint) error {
	close(c.entered)
	<-c.release
	if c.err != nil {
		return c.err
	}
	return c.MemoryTreeCommitter.CommitCheckpoint(ctx, checkpoint)
}
