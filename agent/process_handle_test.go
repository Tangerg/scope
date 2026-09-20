package agent

import (
	"errors"
	"testing"
)

func TestProcessHandleCompletionIsOrderedAndRetainsFirstOutcome(t *testing.T) {
	process := admissionTestProcess(t, 0)
	handle := process.handle
	cause := errors.New("committer unavailable")
	failure := &RuntimeError{processID: handle.processID, cause: cause}
	if !handle.publishRuntimeFailure(failure) || handle.publishResult(Result{}) {
		t.Fatal("outcome publication did not retain the first result")
	}
	handle.finishBookkeeping()
	handle.finishBookkeeping()
	if !handle.finishJoin(failure) || handle.finishJoin(nil) {
		t.Fatal("join publication did not retain the first failure")
	}
	if !errors.Is(handle.joinError(), cause) {
		t.Fatal("join failure was overwritten")
	}
	if _, err := handle.outcome(); !errors.Is(err, cause) {
		t.Fatal("runtime outcome was overwritten")
	}
}

func TestProcessHandleRejectsCompletionBeforePrerequisite(t *testing.T) {
	for _, boundary := range []string{"bookkeeping", "join"} {
		t.Run(boundary, func(t *testing.T) {
			handle := admissionTestProcess(t, 0).handle
			defer func() {
				if recover() == nil {
					t.Fatal("completion skipped its prerequisite")
				}
				select {
				case <-handle.bookkeepingDone:
					t.Fatal("rejected completion closed bookkeeping")
				default:
				}
				if handle.joinDone() {
					t.Fatal("rejected completion closed join")
				}
			}()
			if boundary == "bookkeeping" {
				handle.finishBookkeeping()
			} else {
				handle.finishJoin(nil)
			}
		})
	}
}
