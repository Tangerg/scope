package agent

import "testing"

func TestCanceledCaptureReleasesQueuedFreezeResult(t *testing.T) {
	runtime, process := newChildCompletionTestProcess(t)
	acquisition := &treeFreezeAcquisition{
		response: make(chan treeFreezeAcquisitionResult, 1),
		canceled: make(chan struct{}),
	}
	runtime.acquireFreeze(acquisition)
	if len(acquisition.response) != 1 || !runtime.freezeActive.Load() {
		t.Fatal("capture did not queue its acquired freeze")
	}
	// The caller can select cancellation while its answer is still buffered.
	close(acquisition.canceled)
	if !runtime.tryFreezeCancellation() || runtime.freezeActive.Load() {
		t.Fatal("canceled capture retained the queued freeze")
	}
	if !runtime.advanceOne() || process.attemptSequence == 0 {
		t.Fatal("canceled capture prevented the tree from resuming work")
	}
	runtime.applyCompletion(receiveTreeRuntimeProbe(t, runtime.completions))
	for range schedulingProgressTurns {
		runtime.advanceReadyWork()
		if runtime.commit != nil {
			runtime.applyTreeCommitCompletion(receiveTreeRuntimeProbe(t, runtime.commitDone))
		}
	}
	result, err := process.handle.outcome()
	if err != nil || result.Status() != StatusCompleted {
		t.Fatalf("resumed tree status=%s error=%v", result.Status(), err)
	}
}
