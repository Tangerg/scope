package agent

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRestoreCompletesWithEarlierWallTime(t *testing.T) {
	for _, durable := range []bool{false, true} {
		for _, cancel := range []bool{false, true} {
			t.Run(testWallTimeMode(durable, cancel), func(t *testing.T) {
				deployment, snapshot, config := clockSkewedTree(t, durable, false)
				engine, err := NewEngine(config)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { mustCloseEngine(t, engine) })
				root, err := engine.RestoreTree(t.Context(), deployment, snapshot)
				if err != nil {
					t.Fatal(err)
				}
				want := StatusCompleted
				if cancel {
					want = StatusCanceled
					err = root.RequestCancellation(t.Context(), "stop restored work")
				} else {
					err = root.Resume(t.Context())
				}
				if err != nil {
					t.Fatal(err)
				}
				result := awaitResult(t, root)
				if !result.Valid() || result.Status() != want || !result.FinishedAt().Before(result.StartedAt()) {
					t.Fatalf("result = %#v, want valid %s with earlier finish time", result, want)
				}
				captured := wallTimeSnapshot(t, engine, root, config)
				if _, parseErr := ParseTreeSnapshot(captured.JSON()); parseErr != nil {
					t.Fatalf("terminal snapshot rejected clock skew: %v", parseErr)
				}
				wire, err := captured.ProcessSnapshots()[0].wire()
				if err != nil || wire.StartedAt != result.StartedAt() || *wire.FinishedAt != result.FinishedAt() {
					t.Fatalf("snapshot changed observed times: %v, %v", wire, err)
				}
				wire.FinishedAt = new(time.Time{})
				if _, err := newProcessSnapshot(wire); !errors.Is(err, ErrInvalidSnapshot) {
					t.Fatalf("missing terminal time accepted: %v", err)
				}
				result.finishedAt = time.Time{}
				if result.Valid() {
					t.Fatal("missing result finish time accepted")
				}
				if err := root.Join(t.Context()); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestRestoreAcceptsChildrenWithEarlierWallTimes(t *testing.T) {
	deployment, snapshot, config := clockSkewedTree(t, true, true)
	engine, err := NewEngine(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustCloseEngine(t, engine) })
	root, err := engine.RestoreTree(t.Context(), deployment, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, childSnapshot := range snapshot.ProcessSnapshots() {
		if childSnapshot.ProcessID() == root.ID() {
			continue
		}
		child, exists := engine.Process(childSnapshot.ProcessID())
		if !exists || !child.StartedAt().Before(root.StartedAt()) {
			t.Fatal("restoration lost the recorded child start time")
		}
		if err := child.Resume(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if result := awaitResult(t, root); !result.Valid() || result.Status() != StatusCompleted {
		t.Fatalf("restored tree result = %#v", result)
	}
	if err := root.Join(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func testWallTimeMode(durable, cancel bool) string {
	mode := "ephemeral/"
	if durable {
		mode = "durable/"
	}
	if cancel {
		return mode + "cancel"
	}
	return mode + "complete"
}

func clockSkewedTree(t *testing.T, durable, children bool) (Deployment, TreeSnapshot, EngineConfig) {
	t.Helper()
	config := EngineConfig{}
	if durable {
		config.TreeDurability = &recordingTreeDurability{}
	}
	engine, err := NewEngine(config)
	if err != nil {
		t.Fatal(err)
	}
	deployment := newChildTestDeployment(t)
	mode, status := "leaf_pause", StatusPaused
	if children {
		mode, status = "wait:paused", StatusWaiting
	}
	input, err := EncodeInput(childTestInput{Mode: mode})
	if err != nil {
		t.Fatal(err)
	}
	root, err := engine.Start(t.Context(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 2*time.Second)
		defer cancel()
		if killErr := root.Kill(ctx, "clock fixture cleanup"); killErr != nil && !errors.Is(killErr, ErrProcessFinished) {
			t.Error(killErr)
		}
		if joinErr := root.Join(ctx); joinErr != nil {
			t.Error(joinErr)
		}
		mustCloseEngine(t, engine)
	})
	waitForStatus(t, root, status)
	for _, encoded := range directChildIDs(t, engine, root.ID()) {
		id, parseErr := ParseProcessID(encoded)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		child, _ := engine.Process(id)
		waitForStatus(t, child, StatusPaused)
	}
	snapshot := wallTimeSnapshot(t, engine, root, config)
	wire, err := snapshot.wire()
	if err != nil {
		t.Fatal(err)
	}
	for index, process := range wire.ProcessSnapshots {
		if process.ProcessID() != root.ID() {
			continue
		}
		processWire, wireErr := process.wire()
		if wireErr != nil {
			t.Fatal(wireErr)
		}
		// The restored writer's wall clock is behind the recorded start.
		processWire.StartedAt = time.Now().Add(time.Hour).Round(0).UTC()
		wire.ProcessSnapshots[index], err = newProcessSnapshot(processWire)
		if err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err = newTreeSnapshot(wire)
	if err != nil {
		t.Fatal(err)
	}
	return deployment, snapshot, config
}

func wallTimeSnapshot(t *testing.T, engine *Engine, root *Process, config EngineConfig) TreeSnapshot {
	t.Helper()
	if recorder, ok := config.TreeDurability.(*recordingTreeDurability); ok {
		checkpoints := recorder.treeCheckpoints()
		if len(checkpoints) == 0 {
			t.Fatal("missing acknowledged checkpoint")
		}
		return checkpoints[len(checkpoints)-1].TreeSnapshot()
	}
	snapshot, err := engine.CaptureTree(t.Context(), root.ID())
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
