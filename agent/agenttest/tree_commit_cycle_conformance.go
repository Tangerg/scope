package agenttest

import (
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent"
)

func runCheckpointCycleConformance(t *testing.T, factory func() TreeCommitterConformanceDriver) {
	t.Helper()
	driver := factory()
	probe := newConformanceDurabilityProbe(t, driver)
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: probe})
	if err != nil {
		t.Fatal(err)
	}
	deployment := conformanceDeployment(t, conformanceModeWait)
	input, err := deployment.Descriptor().EncodeInput(conformanceInput{Value: "cycle"})
	if err != nil {
		t.Fatal(err)
	}
	process, err := engine.Start(t.Context(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeSignalConformanceProcess(t, engine, process) })
	waitForConformanceStatus(t, engine, process, agent.StatusWaiting)
	waiting := probe.latestCheckpoint()
	original := inspectConformanceProcess(t, engine, process)
	waitID, ok := original.WaitID()
	if !ok {
		t.Fatal("waiting cut has no WaitID")
	}
	var paused agent.TreeCheckpoint
	for cycle := range 3 {
		if err := process.Pause(t.Context(), "review"); err != nil {
			t.Fatal(err)
		}
		waitForConformanceStatus(t, engine, process, agent.StatusPaused)
		current := probe.latestCheckpoint()
		if current.Sequence() != waiting.Sequence()+uint64(2*cycle)+1 {
			t.Fatalf("pause sequence=%d", current.Sequence())
		}
		if cycle == 0 {
			paused = current
		} else {
			if current.TreeSnapshot().Digest() != paused.TreeSnapshot().Digest() {
				t.Fatal("pause did not repeat the same content")
			}
			if err := driver.CommitCheckpoint(t.Context(), paused); !errors.Is(err, agent.ErrCommitConflict) {
				t.Fatalf("historical pause replay at identical head: %v", err)
			}
		}
		if err := process.Resume(t.Context()); err != nil {
			t.Fatal(err)
		}
		waitForConformanceStatus(t, engine, process, agent.StatusWaiting)
		current = probe.latestCheckpoint()
		if current.Sequence() != waiting.Sequence()+uint64(2*cycle)+2 || current.TreeSnapshot().Digest() != waiting.TreeSnapshot().Digest() {
			t.Fatalf("resume sequence=%d or content differs", current.Sequence())
		}
		snapshot := inspectConformanceProcess(t, engine, process)
		currentWait, ok := snapshot.WaitID()
		if !ok || currentWait != waitID || snapshot.Usage() != original.Usage() {
			t.Fatal("cycle changed the wait or execution progress")
		}
		if err := driver.CommitCheckpoint(t.Context(), waiting); !errors.Is(err, agent.ErrCommitConflict) {
			t.Fatalf("historical waiting replay at identical head: %v", err)
		}
		if err := driver.CommitCheckpoint(t.Context(), paused); !errors.Is(err, agent.ErrCommitConflict) {
			t.Fatalf("historical pause rewound waiting head: %v", err)
		}
		assertCrashHead(t, driver, process.ID(), waiting.TreeSnapshot().Digest())
	}
}
