package agent

import (
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
		if closeErr := engine.Close(); closeErr != nil {
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
	before := process.Usage()
	secondID, _ := ParseSignalID("signal:second-input")
	second, err := NewSignalRequest(secondID, WaitID{}, []byte(`{"value":"second"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, requests := range [][]SignalRequest{{first}, {second, first}, {second, second}} {
		if accepted, deliveryErr := process.DeliverSignals(t.Context(), requests...); deliveryErr != nil || accepted {
			t.Fatalf("duplicate batch = %t, %v", accepted, deliveryErr)
		}
		if usage := process.Usage(); usage != before {
			t.Fatalf("duplicate charged usage: before=%+v after=%+v", before, usage)
		}
	}
	if accepted, deliveryErr := process.DeliverSignals(t.Context(), second); accepted || !errors.Is(deliveryErr, ErrResourceLimitExceeded) {
		t.Fatalf("new signal at capacity = %t, %v", accepted, deliveryErr)
	}
	snapshot, err := process.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	wire, err := snapshot.wire()
	if err != nil || len(wire.Mailbox.Signals) != 2 || wire.Mailbox.Signals[1].Signal.ID() != firstID {
		t.Fatalf("mailbox = %+v, error = %v", wire.Mailbox, err)
	}
	if killErr := process.Kill(t.Context(), "inspection complete"); killErr != nil {
		t.Fatal(killErr)
	}
	if result, awaitErr := process.Await(t.Context()); awaitErr != nil || result.Status() != StatusKilled {
		t.Fatalf("kill result=%s error=%v", result.Status(), awaitErr)
	}
}
