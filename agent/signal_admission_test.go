package agent

import (
	"context"
	"errors"
	"testing"
)

func TestSignalBatchDeduplicatesBeforeChargingFullMailbox(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxSignals = 2
	limits.MaxPendingSignals = 2
	engine, err := NewEngine(EngineConfig{Limits: limits})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	release := make(chan struct{})
	dispatcher := &engineTestDispatcher{
		policy: ReplayPolicySameIdentity, started: make(chan struct{}, 1), block: release,
	}
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	deployment := engineTestDeployment(t, definition, dispatcher)
	input, err := EncodeInput(engineTestInput{Value: "paused"})
	if err != nil {
		t.Fatal(err)
	}
	process, err := engine.Start(t.Context(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	<-dispatcher.started
	if pauseErr := process.Pause(t.Context(), "inspect signal admission"); pauseErr != nil {
		t.Fatal(pauseErr)
	}
	close(release)
	waitForStatus(t, process, StatusPaused)
	firstID, _ := ParseSignalID("signal:first-input")
	first, err := NewSignalRequest(firstID, WaitID{}, []byte(`{"value":"first"}`))
	if err != nil {
		t.Fatal(err)
	}
	if accepted, deliveryErr := process.DeliverSignals(t.Context(), first); deliveryErr != nil || !accepted {
		t.Fatalf("first delivery = %t, %v", accepted, deliveryErr)
	}
	before := inspectProcessSnapshot(t, process).Usage()
	secondID, _ := ParseSignalID("signal:second-input")
	second, err := NewSignalRequest(secondID, WaitID{}, []byte(`{"value":"second"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, requests := range [][]SignalRequest{{first}, {second, first}, {second, second}} {
		if accepted, deliveryErr := process.DeliverSignals(t.Context(), requests...); deliveryErr != nil || accepted {
			t.Fatalf("duplicate batch = %t, %v", accepted, deliveryErr)
		}
		if usage := inspectProcessSnapshot(t, process).Usage(); usage != before {
			t.Fatalf("duplicate charged usage: before=%+v after=%+v", before, usage)
		}
	}
	conflictingFirst, err := NewSignalRequest(firstID, WaitID{}, []byte(`{"value":"conflict"}`))
	if err != nil {
		t.Fatal(err)
	}
	conflictingSecond, err := NewSignalRequest(secondID, WaitID{}, []byte(`{"value":"conflict"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, requests := range [][]SignalRequest{{first, conflictingFirst}, {second, conflictingSecond}} {
		if accepted, deliveryErr := process.DeliverSignals(t.Context(), requests...); accepted || !errors.Is(deliveryErr, ErrSignalConflict) {
			t.Fatalf("conflicting batch = %t, %v", accepted, deliveryErr)
		}
		if inspectProcessSnapshot(t, process).Usage() != before {
			t.Fatal("conflicting batch changed usage")
		}
	}
	if accepted, deliveryErr := process.DeliverSignals(t.Context(), second); accepted || !errors.Is(deliveryErr, ErrResourceLimitExceeded) {
		t.Fatalf("new signal at capacity = %t, %v", accepted, deliveryErr)
	}
	snapshot := inspectProcessSnapshot(t, process)
	wire, err := snapshot.wire()
	if err != nil || len(wire.Mailbox.Signals) != 2 || wire.Mailbox.Signals[1].ID != firstID {
		t.Fatalf("mailbox = %+v, error = %v", wire.Mailbox, err)
	}
	if killErr := process.Kill(t.Context(), "inspection complete"); killErr != nil {
		t.Fatal(killErr)
	}
	if result, awaitErr := process.Await(t.Context()); awaitErr != nil || result.Status() != StatusKilled {
		t.Fatalf("kill result=%s error=%v", result.Status(), awaitErr)
	}
}
