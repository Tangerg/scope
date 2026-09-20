package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestSignalBatchPublishesOnlyNewIdentities(t *testing.T) {
	for _, recording := range []bool{false, true} {
		t.Run(fmt.Sprintf("recording_%t", recording), func(t *testing.T) {
			listener := &recordingEventListener{}
			config := EngineConfig{TreeCommitter: NewMemoryTreeCommitter(), EventListeners: []EventListener{listener}}
			if recording {
				config.TreeCommitter = &recordingTreeCommitter{}
			}
			engine := controlValue(NewEngine(config))
			t.Cleanup(func() {
				if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
					t.Error(err)
				}
			})
			definition := &rejectedStepDefinition{descriptor: newEngineTestDefinition(t, "test.batch", "complete").Descriptor()}
			deployment := engineTestDeployment(t, definition, nil)
			process := controlValue(engine.Start(t.Context(), deployment, controlValue(EncodePayload(engineTestInput{}))))
			waitForStatus(t, process, StatusPaused)
			first := controlValue(NewSignalRequest(controlValue(ParseSignalID("signal:first")), WaitID{}, []byte(`{}`)))
			second := controlValue(NewSignalRequest(controlValue(ParseSignalID("signal:second")), WaitID{}, []byte(`{}`)))
			if accepted, err := process.DeliverSignals(t.Context(), first, first); accepted || !errors.Is(err, ErrSignalConflict) {
				t.Fatalf("repeated new identity: %t %v", accepted, err)
			}
			for _, batch := range [][]SignalRequest{{first}, {first, second}} {
				if accepted, err := process.DeliverSignals(t.Context(), batch...); !accepted || err != nil {
					t.Fatalf("new admission: %t %v", accepted, err)
				}
			}
			before := inspectProcessSnapshot(t, process)
			if accepted, err := process.DeliverSignals(t.Context(), first, second); accepted || err != nil {
				t.Fatalf("historical replay: %t %v", accepted, err)
			}
			after := inspectProcessSnapshot(t, process)
			receipts := after.SignalReceipts()
			if before.Usage() != after.Usage() || after.Usage().AcceptedSignals != 2 || len(receipts) != 2 ||
				!receipts[0].Matches(first) || !receipts[1].Matches(second) {
				t.Fatal("batch lost identity, order, or charged replay")
			}
			var ids []SignalID
			for _, event := range listener.snapshot() {
				if fact, accepted := event.SignalAccepted(); accepted {
					ids = append(ids, fact.SignalID())
				}
			}
			if len(ids) != 2 || ids[0] != first.ID() || ids[1] != second.ID() {
				t.Fatalf("acceptance events: %v", ids)
			}
			if err := process.Kill(t.Context(), "complete test"); err != nil {
				t.Fatal(err)
			}
			if err := process.Join(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSignalBatchIdentityContract(t *testing.T) {
	for _, repeated := range []bool{false, true} {
		process := admissionTestProcess(t, 1)
		previous := mustMailboxSignal(t, "signal:0", WaitID{}, []byte(`{}`))
		fresh := mustMailboxSignal(t, "signal:fresh", WaitID{}, []byte(`{}`))
		signals := []Signal{previous, fresh}
		if repeated {
			signals = []Signal{previous, previous, fresh}
		}
		accepted, err := admitTestSignals(process, signals, signalSourceExternal)
		if repeated {
			if accepted || !errors.Is(err, ErrSignalConflict) || process.mailbox.acceptedCount() != 1 {
				t.Fatalf("repeated identity: accepted=%t error=%v count=%d", accepted, err, process.mailbox.acceptedCount())
			}
			continue
		}
		if !accepted || err != nil || process.mailbox.acceptedCount() != 2 {
			t.Fatalf("mixed history: accepted=%t error=%v count=%d", accepted, err, process.mailbox.acceptedCount())
		}
		if accepted, err := admitTestSignals(process, signals, signalSourceExternal); accepted || err != nil {
			t.Fatalf("replay: accepted=%t error=%v", accepted, err)
		}
	}
}

func TestUnsupportedDeadlineFactsAreRejected(t *testing.T) {
	if _, err := newDeadlineIntent(deadlineOwner("process"), "deadline reached"); !errors.Is(err, errInvalidTermination) {
		t.Errorf("unsupported deadline intent: %v", err)
	}
	encoded := []byte(`{"status":"timed_out","cause":"process_deadline","reason":"deadline reached"}`)
	var termination Termination
	if err := json.Unmarshal(encoded, &termination); !errors.Is(err, errInvalidTermination) {
		t.Errorf("unsupported termination: %v", err)
	}
	if _, err := decodeProcessFinishedFact(controlValue(json.Marshal(processFinishedEventPayload{ProcessStatus: StatusTimedOut, TerminationCause: TerminationCause("process_deadline"), Usage: new(Usage)}))); err == nil {
		t.Error("finished fact accepted unsupported deadline")
	}
	process := admissionTestProcess(t, 0)
	wire := controlValue(controlValue(process.capture()).wire())
	wire.PendingControl = pendingControlWire{DeadlineOwner: deadlineOwner("process"), DeadlineReason: "deadline reached"}
	if _, err := ParseProcessSnapshot(controlValue(json.Marshal(wire))); !errors.Is(err, ErrInvalidSnapshot) {
		t.Errorf("snapshot accepted unsupported deadline: %v", err)
	}
}
