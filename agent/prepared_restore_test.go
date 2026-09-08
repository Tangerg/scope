package agent

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestRestoreRejectsUnrestorableCandidateBeforeExternalWork(t *testing.T) {
	snapshot := preparedEngineTestSnapshot(t)
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	for _, test := range []struct {
		name    string
		durable bool
		phase   effectPhase
	}{
		{name: "ephemeral planned", phase: effectPhasePlanned},
		{name: "ephemeral pending", phase: effectPhasePending},
		{name: "durable planned", durable: true, phase: effectPhasePlanned},
		{name: "durable pending", durable: true, phase: effectPhasePending},
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
			treeWire := treeSnapshotWire{RootID: invalid.ProcessID(), ProcessSnapshots: []ProcessSnapshot{invalid}}
			var config EngineConfig
			durability := &recordingTreeDurability{}
			if test.durable {
				incarnation, incarnationErr := newTreeIncarnationID()
				if incarnationErr != nil {
					t.Fatal(incarnationErr)
				}
				treeWire.IncarnationID = &incarnation
				config.TreeDurability = durability
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
				if closeErr := engine.Close(); closeErr != nil {
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
			if len(durability.treeActivations()) != 0 || len(durability.effectBoundaries()) != 0 || len(durability.treeCheckpoints()) != 0 {
				t.Error("invalid candidate reached the durability boundary")
			}
		})
	}
}
