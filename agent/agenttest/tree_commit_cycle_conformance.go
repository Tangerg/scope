package agenttest

import (
	"errors"
	"testing"

	agent "github.com/Tangerg/scope/agent"
)

func runCheckpointCycleConformance(t *testing.T, factory func() TreeCommitterConformanceDriver) {
	t.Helper()
	driver := factory()
	probe := newConformanceDurabilityProbe(t, driver)
	engine := newConformanceEngine(t, probe)
	deployment := conformanceDeployment(t, conformanceModeWait)
	process := startConformanceProcess(t, engine, deployment, "cycle")
	t.Cleanup(func() { closeConformanceProcess(t, engine, process) })
	waitForConformanceStatus(t, engine, process, agent.StatusWaiting)
	waiting := probe.latestCheckpoint()
	original := inspectConformanceProcess(t, engine, process)
	waitID, ok := original.WaitID()
	if !ok {
		t.Fatal("waiting cut has no WaitID")
	}
	var paused agent.TreeCheckpoint
	for cycle := range 3 {
		current := pauseConformanceCycle(t, engine, probe, process, waiting.Sequence()+uint64(2*cycle)+1)
		if cycle == 0 {
			paused = current
		} else if current.TreeSnapshot().Digest() != paused.TreeSnapshot().Digest() {
			t.Fatal("pause did not repeat the same content")
		} else if err := driver.CommitCheckpoint(t.Context(), paused); !errors.Is(err, agent.ErrCommitConflict) {
			t.Fatalf("historical pause replay at identical head: %v", err)
		}
		resumeConformanceCycle(t, engine, probe, process, waiting, waiting.Sequence()+uint64(2*cycle)+2)
		snapshot := inspectConformanceProcess(t, engine, process)
		if currentWait, ok := snapshot.WaitID(); !ok || currentWait != waitID || snapshot.Usage() != original.Usage() {
			t.Fatal("cycle changed the wait or execution progress")
		}
		if err := driver.CommitCheckpoint(t.Context(), waiting); !errors.Is(err, agent.ErrCommitConflict) {
			t.Fatalf("historical waiting replay at identical head: %v", err)
		}
		if err := driver.CommitCheckpoint(t.Context(), paused); !errors.Is(err, agent.ErrCommitConflict) {
			t.Fatalf("historical pause rewound waiting head: %v", err)
		}
		assertCrashHead(t, driver, process.Relation().ProcessID(), waiting.TreeSnapshot().Digest())
	}
}

func pauseConformanceCycle(
	t *testing.T,
	engine *agent.Engine,
	probe *conformanceDurabilityProbe,
	process *agent.Process,
	wantSequence uint64,
) agent.TreeCheckpoint {
	t.Helper()
	if err := process.Pause(t.Context(), "review"); err != nil {
		t.Fatal(err)
	}
	waitForConformanceStatus(t, engine, process, agent.StatusPaused)
	current := probe.latestCheckpoint()
	if current.Sequence() != wantSequence {
		t.Fatalf("pause sequence=%d", current.Sequence())
	}
	return current
}

func resumeConformanceCycle(
	t *testing.T,
	engine *agent.Engine,
	probe *conformanceDurabilityProbe,
	process *agent.Process,
	waiting agent.TreeCheckpoint,
	wantSequence uint64,
) {
	t.Helper()
	if err := process.Resume(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitForConformanceStatus(t, engine, process, agent.StatusWaiting)
	current := probe.latestCheckpoint()
	if current.Sequence() != wantSequence || current.TreeSnapshot().Digest() != waiting.TreeSnapshot().Digest() {
		t.Fatalf("resume sequence=%d or content differs", current.Sequence())
	}
}
