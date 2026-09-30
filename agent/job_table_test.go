package agent

import "testing"

func TestJobTableRetiresOnlyTheMatchingAttempt(t *testing.T) {
	processID := newProcessID()
	table := newJobTable(1)
	canceled := false
	job := &processJob{kind: processJobRestore, attempt: 2, cancel: func() { canceled = true }}
	table.start(processID, job)
	for _, stale := range []treeJobCompletion{
		{processID: processID, attempt: 1, result: restoreJobResult{}},
		{processID: processID, attempt: 2, result: stepJobResult{}},
	} {
		if _, current := table.finish(stale); current {
			t.Fatalf("mismatched completion %+v retired the job", stale)
		}
	}
	if table.active.Load() != 1 || table.get(processID) != job || canceled {
		t.Fatal("mismatched completion changed the in-flight job")
	}
	retired, current := table.finish(treeJobCompletion{processID: processID, attempt: 2, result: restoreJobResult{}})
	if !current || retired != job || !canceled || !table.empty() || table.active.Load() != 0 {
		t.Fatal("matching completion did not retire and cancel the job")
	}
}

func TestProcessJobStopSemanticsFollowItsKind(t *testing.T) {
	effectID := newProcessID().effectID(1, 0)
	for _, test := range []struct {
		kind            processJobKind
		interruptStale  bool
		uncertainEffect bool
	}{
		{processJobStep, true, false},
		{processJobRestore, true, false},
		{processJobDispatch, false, true},
		{processJobChildStart, false, true},
	} {
		interrupted := &processJob{kind: test.kind, effectID: effectID}
		interrupted.interrupt()
		abandoned := &processJob{kind: test.kind}
		abandoned.abandon()
		_, uncertain := interrupted.uncertainEffect()
		if interrupted.stale != test.interruptStale || !abandoned.stale || uncertain != test.uncertainEffect {
			t.Errorf("kind %d: interrupt stale=%t abandon stale=%t uncertain=%t", test.kind, interrupted.stale, abandoned.stale, uncertain)
		}
	}
}
