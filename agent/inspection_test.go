package agent

import (
	"context"
	"testing"
	"time"
)

func inspectProcessSnapshot(t testing.TB, process *Process) ProcessSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	return inspectProcessSnapshotContext(ctx, t, process)
}

func inspectProcessSnapshotContext(ctx context.Context, t testing.TB, process *Process) ProcessSnapshot {
	t.Helper()
	runtime := process.handle.treeRuntime()
	if runtime == nil {
		t.Fatal("Process tree was released before inspection")
	}
	inspection, err := runtime.engine.InspectTree(ctx, process.Relation().RootID())
	if err != nil {
		t.Fatal(err)
	}
	report, found := inspection.Process(process.Relation().ProcessID())
	if !found {
		t.Fatal("Process was not published in the inspected tree")
	}
	return report.Snapshot
}
