package agent

import (
	"context"
	"testing"
	"time"
)

func inspectProcessSnapshot(t testing.TB, process *Process) ProcessSnapshot {
	t.Helper()
	runtime := process.handle.runtime.Load()
	if runtime == nil {
		t.Fatal("Process tree was released before inspection")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	inspection, err := runtime.engine.InspectTree(ctx, process.Relation().RootID())
	if err != nil {
		t.Fatal(err)
	}
	report, found := inspection.Process(process.ID())
	if !found {
		t.Fatal("Process was not published in the inspected tree")
	}
	return report.Snapshot
}
