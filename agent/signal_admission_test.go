package agent

import (
	"context"
	"errors"
	"testing"
)

func TestSignalBatchDeduplicatesBeforeChargingFullMailbox(t *testing.T) {
	limits := DefaultLimits()
	limits.Budget.Signals = NewQuota(2)
	limits.MaxPendingSignals = 2
	engine, err := NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter(), Limits: limits})
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
	input, err := EncodePayload(engineTestInput{Value: "paused"})
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
	for _, test := range []struct {
		requests []SignalRequest
		want     error
	}{
		{requests: []SignalRequest{first}},
		{requests: []SignalRequest{second, first}, want: ErrResourceLimitExceeded},
		{requests: []SignalRequest{second, second}, want: ErrSignalConflict},
	} {
		if accepted, deliveryErr := process.DeliverSignals(t.Context(), test.requests...); !errors.Is(deliveryErr, test.want) || accepted {
			t.Fatalf("batch = %t, %v; want false, %v", accepted, deliveryErr, test.want)
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

func TestSignalSchemaRejectionIsAtomic(t *testing.T) {
	definition := &rejectedStepDefinition{descriptor: controlValue(NewDescriptor(DescriptorConfig{
		Name: "test.signal_schema", Description: "Accept string signals while paused.",
		InputSchema: controlValue(SchemaFor[engineTestInput]()), OutputSchema: controlValue(SchemaFor[engineTestOutput]()),
		SignalSchema: controlValue(SchemaFor[string]()),
	})), err: errors.New("test finished")}
	engine := controlValue(NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter()}))
	defer mustCloseEngine(t, engine)
	deployment := engineTestDeployment(t, definition, nil)
	process := controlValue(engine.Start(t.Context(), deployment, controlValue(EncodePayload(engineTestInput{}))))
	waitForStatus(t, process, StatusPaused)
	before := inspectProcessSnapshot(t, process)
	valid := controlValue(NewSignalRequest(controlValue(ParseSignalID("signal:valid")), WaitID{}, []byte(`"input"`)))
	invalid := controlValue(NewSignalRequest(controlValue(ParseSignalID("signal:invalid")), WaitID{}, []byte(`42`)))
	if accepted, err := process.DeliverSignals(t.Context(), valid, invalid); accepted || !errors.Is(err, ErrSignalRejected) {
		t.Fatalf("schema rejection=%t %v", accepted, err)
	}
	after := inspectProcessSnapshot(t, process)
	if after.Usage() != before.Usage() || len(after.SignalReceipts()) != 0 || after.Status() != StatusPaused {
		t.Fatal("rejected batch changed mailbox, budget, or status")
	}
	if accepted, err := process.DeliverSignals(t.Context(), valid); !accepted || err != nil {
		t.Fatalf("valid input=%t %v", accepted, err)
	}
	conflict := controlValue(NewSignalRequest(valid.ID(), WaitID{}, []byte(`42`)))
	if accepted, err := process.DeliverSignals(t.Context(), conflict); accepted || !errors.Is(err, ErrSignalConflict) {
		t.Fatalf("conflicting identity must be rejected before schema validation: %t %v", accepted, err)
	}
	if accepted, err := process.DeliverSignals(t.Context(), valid, invalid); accepted || !errors.Is(err, ErrSignalRejected) {
		t.Fatalf("duplicate identity must not bypass batch schema validation: %t %v", accepted, err)
	}
	wire := controlValue(inspectProcessSnapshot(t, process).wire())
	wire.Mailbox.Signals[0] = mailboxRecordWire(1, controlValue(NewSignal(invalid.ID(), WaitID{}, invalid.Payload())))
	tampered := controlValue(newProcessSnapshot(wire))
	if _, _, _, err := prepareRestoredProcess(t.Context(), deployment, tampered); !errors.Is(err, ErrInvalidSnapshot) || !errors.Is(err, ErrSignalRejected) {
		t.Fatalf("restoration bypassed the declared Signal schema: %v", err)
	}
	if err := process.Kill(t.Context(), "finished"); err != nil {
		t.Fatal(err)
	}
	if err := process.Join(t.Context()); err != nil {
		t.Fatal(err)
	}
}
