package agent

import (
	"bytes"
	"testing"
)

func TestFinalizationFailureRetainsSettlementsWithoutAdoptingCandidate(t *testing.T) {
	for _, durable := range []bool{false, true} {
		name := "ephemeral"
		if durable {
			name = "durable"
		}
		t.Run(name, func(t *testing.T) {
			config := EngineConfig{}
			if durable {
				config.TreeDurability = &recordingTreeDurability{}
			}
			engine, err := NewEngine(config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { mustCloseEngine(t, engine) })
			key, err := ParseWaitKey("duplicate")
			if err != nil {
				t.Fatal(err)
			}
			wait, err := RequestWait(key, []byte(`{"request":"answer"}`))
			if err != nil {
				t.Fatal(err)
			}
			external, err := NewDispatcherEffect([]byte(`{"kind":"effect","value":"retained"}`))
			if err != nil {
				t.Fatal(err)
			}
			definition := &effectSequenceDefinition{
				descriptor: newEngineTestDefinition(t, "engine.finalization", "effect").Descriptor(),
				effects:    []Effect{external, wait, wait},
			}
			dispatcher := &engineTestDispatcher{policy: ReplayPolicySameIdentity}
			deployment := engineTestDeployment(t, definition, dispatcher)
			input, err := EncodeInput(engineTestInput{Value: "retained"})
			if err != nil {
				t.Fatal(err)
			}
			process, err := engine.Start(t.Context(), deployment, input)
			if err != nil {
				t.Fatal(err)
			}
			result := mustAwait(t, process)
			failure, failed := result.Termination().Failure()
			if result.Status() != StatusFailed || !failed || failure.Code() != "engine.finalize.invalid" {
				t.Fatalf("finalization result=%s failure=%+v", result.Status(), failure)
			}
			snapshot := inspectProcessSnapshot(t, process)
			wire, err := snapshot.wire()
			if err != nil {
				t.Fatal(err)
			}
			if wire.Prepared == nil || len(wire.Prepared.Effects) != 3 {
				t.Fatal("failed finalization erased the performed operations")
			}
			settled := wire.Prepared.Effects[0]
			if !settled.definitelySettled() || settled.Settlement.Status() != SettlementStatusSucceeded ||
				string(settled.Settlement.Payload()) != `{"kind":"result","value":"retained:done"}` {
				t.Fatalf("external settlement changed: %+v", settled)
			}
			state, err := wireJSON.decode[engineTestState](wire.CommittedExecutionState.Payload())
			if err != nil || state.Phase != "ready" || wire.Usage != (Usage{PreparedEffects: 3}) ||
				wire.Mailbox.SignalCursor != 0 || len(wire.Mailbox.Signals) != 0 || len(wire.Mailbox.Waits) != 0 {
				t.Fatalf("failed finalization adopted candidate state: %+v, %v", wire, err)
			}
			tree := interruptedTreeSnapshot(t, engine, process, config)
			restoredEngine, err := NewEngine(config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { mustCloseEngine(t, restoredEngine) })
			restored, err := restoredEngine.RestoreTree(t.Context(), deployment, tree)
			if err != nil {
				t.Fatal(err)
			}
			if got := mustAwait(t, restored); got.Status() != result.Status() || got.Usage() != result.Usage() {
				t.Fatalf("restored failure changed: %+v", got)
			}
			if !bytes.Equal(inspectProcessSnapshot(t, restored).JSON(), snapshot.JSON()) || dispatcher.calls.Load() != 1 {
				t.Fatal("restoration changed settlement evidence or repeated the external operation")
			}
		})
	}
}
