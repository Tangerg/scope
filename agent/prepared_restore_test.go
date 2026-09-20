package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestRestoreRejectsUnrestorableCandidateBeforeExternalWork(t *testing.T) {
	snapshot := preparedEngineTestSnapshot(t)
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	for _, test := range []struct {
		name      string
		recording bool
		phase     effectPhase
	}{
		{name: "memory planned", phase: effectPhasePlanned},
		{name: "memory pending", phase: effectPhasePending},
		{name: "recording planned", recording: true, phase: effectPhasePlanned},
		{name: "recording pending", recording: true, phase: effectPhasePending},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire, err := snapshot.wire()
			if err != nil {
				t.Fatal(err)
			}
			wire.Prepared.CandidateState, err = NewExecutionState(definition.Descriptor().Name(), json.RawMessage(`{"phase":7,"value":"invalid candidate"}`))
			if err != nil {
				t.Fatal(err)
			}
			wire.Prepared.Effects[0].Phase = test.phase
			invalid, err := newProcessSnapshot(wire)
			if err != nil {
				t.Fatal(err)
			}
			treeWire := treeSnapshotWire{IncarnationID: newTreeIncarnationID(), RootID: invalid.ProcessID(), ProcessSnapshots: []ProcessSnapshot{invalid}}
			config := EngineConfig{TreeCommitter: NewMemoryTreeCommitter()}
			committer := &recordingTreeCommitter{}
			if test.recording {
				incarnation := newTreeIncarnationID()
				treeWire.IncarnationID = incarnation
				config.TreeCommitter = committer
			}
			encoded, err := json.Marshal(treeWire)
			if err != nil {
				t.Fatal(err)
			}
			tree, err := ParseTreeSnapshot(encoded)
			if err != nil {
				t.Fatal(err)
			}
			dispatcher := &engineTestDispatcher{policy: ReplayPolicySameIdentity}
			deployment := engineTestDeployment(t, definition, dispatcher)
			engine, err := NewEngine(config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
					t.Error(closeErr)
				}
			})
			process, err := engine.RestoreTree(t.Context(), deployment, tree)
			if process != nil {
				awaitResult(t, process)
			}
			if process != nil || !errors.Is(err, ErrInvalidTreeSnapshot) || !errors.Is(err, ErrInvalidSnapshot) {
				t.Errorf("RestoreTree = %v, %v; want invalid snapshot rejection", process, err)
			}
			if _, registered := engine.Process(tree.RootID()); registered {
				t.Error("invalid candidate published a Process")
			}
			if calls := dispatcher.calls.Load(); calls != 0 {
				t.Errorf("invalid candidate dispatched %d Effects", calls)
			}
			if len(committer.treeActivations()) != 0 || len(committer.effectBoundaries()) != 0 || len(committer.treeCheckpoints()) != 0 {
				t.Error("invalid candidate reached the committer boundary")
			}
		})
	}
}

func TestPreparedSnapshotEnforcesEffectCapabilities(t *testing.T) {
	snapshot := preparedEngineTestSnapshot(t)
	wire, err := snapshot.wire()
	if err != nil {
		t.Fatal(err)
	}
	required, err := ParseCapability("resource.read")
	if err != nil {
		t.Fatal(err)
	}
	effect, err := NewDispatcherEffect(wire.Prepared.Effects[0].Effect.Payload(), required)
	if err != nil {
		t.Fatal(err)
	}
	wire.Prepared.Effects[0].Effect = effect
	wire.Prepared.Intent, err = Continue(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, allowed := range []bool{false, true} {
		if allowed {
			wire.Capabilities, err = NewCapabilitySet(required)
			if err != nil {
				t.Fatal(err)
			}
		}
		encoded, encodeErr := json.Marshal(wire)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		_, parseErr := ParseProcessSnapshot(encoded)
		if allowed {
			if parseErr != nil {
				t.Fatalf("granted Effect rejected: %v", parseErr)
			}
		} else if !errors.Is(parseErr, ErrInvalidSnapshot) || !errors.Is(parseErr, ErrInvalidCapability) {
			t.Errorf("ungranted Effect parse error = %v; want invalid capability snapshot", parseErr)
		}
	}
}

func TestRestorePreparedOutputUsesDeploymentSchema(t *testing.T) {
	snapshot := preparedEngineTestSnapshot(t)
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	for _, test := range []struct {
		name    string
		payload string
		valid   bool
	}{
		{name: "invalid", payload: `{"value":7}`},
		{name: "valid", payload: `{"value":"restored output"}`, valid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire, err := snapshot.wire()
			if err != nil {
				t.Fatal(err)
			}
			output, err := ParsePayload(json.RawMessage(test.payload))
			if err != nil {
				t.Fatal(err)
			}
			wire.Prepared.Intent, err = Complete(0, output)
			if err != nil {
				t.Fatal(err)
			}
			wire.Prepared.Effects = nil
			wire.Counters.PreparedEffects = 0
			prepared, err := newProcessSnapshot(wire)
			if err != nil {
				t.Fatal(err)
			}
			incarnation := newTreeIncarnationID()
			tree, err := newTreeSnapshot(treeSnapshotWire{
				RootID: prepared.ProcessID(), IncarnationID: incarnation,
				ProcessSnapshots: []ProcessSnapshot{prepared},
			})
			if err != nil {
				t.Fatal(err)
			}
			committer := &recordingTreeCommitter{}
			engine, err := NewEngine(EngineConfig{TreeCommitter: committer})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
					t.Error(closeErr)
				}
			})
			dispatcher := &engineTestDispatcher{policy: ReplayPolicySameIdentity}
			deployment := engineTestDeployment(t, definition, dispatcher)
			process, err := engine.RestoreTree(t.Context(), deployment, tree)
			if test.valid {
				if err != nil {
					t.Fatal(err)
				}
				result := awaitResult(t, process)
				restoredOutput, hasOutput := result.Output()
				if result.Status() != StatusCompleted || !hasOutput || string(restoredOutput.JSON()) != test.payload {
					t.Fatalf("restored output = %s, %s", result.Status(), restoredOutput.JSON())
				}
				return
			}
			if process != nil {
				awaitResult(t, process)
			}
			if process != nil || !errors.Is(err, ErrInvalidTreeSnapshot) || !errors.Is(err, ErrInvalidPayload) {
				t.Errorf("RestoreTree = %v, %v; want invalid output rejection", process, err)
			}
			if _, registered := engine.Process(tree.RootID()); registered {
				t.Error("invalid output published a Process")
			}
			if len(committer.treeActivations()) != 0 || len(committer.treeCheckpoints()) != 0 {
				t.Error("invalid output reached the committer boundary")
			}
		})
	}
}
