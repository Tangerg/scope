package agent

import (
	"context"
	"errors"
	"testing"
	"time"
)

func inspectProcessSnapshot(t testing.TB, process *Process) ProcessSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	snapshot, err := inspectProcessSnapshotContext(ctx, process)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func inspectProcessSnapshotContext(ctx context.Context, process *Process) (ProcessSnapshot, error) {
	runtime := process.handle.treeRuntime()
	if runtime == nil {
		return ProcessSnapshot{}, errors.New("Process tree was released before inspection")
	}
	inspection, err := runtime.engine.InspectTree(ctx, process.Relation().RootID())
	if err != nil {
		return ProcessSnapshot{}, err
	}
	report, found := inspection.Process(process.Relation().ProcessID())
	if !found {
		return ProcessSnapshot{}, errors.New("Process was not published in the inspected tree")
	}
	return report.Snapshot, nil
}
