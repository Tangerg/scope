package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestSnapshotsRejectImpossibleWaitState(t *testing.T) {
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	definition := newEngineTestDefinition(t, "engine.wait", "wait")
	deployment := engineTestDeployment(t, definition, &engineTestDispatcher{policy: ReplayPolicyNever})
	input, _ := EncodeInput(engineTestInput{Value: "snapshot"})
	process, err := engine.Start(t.Context(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if killErr := process.Kill(context.Background(), "test complete"); killErr != nil && !errors.Is(killErr, ErrProcessFinished) {
			t.Error(killErr)
		}
		awaitResult(t, process)
	})
	waitForStatus(t, process, StatusWaiting)
	waiting, err := engine.CaptureTree(t.Context(), process.ID())
	if err != nil {
		t.Fatal(err)
	}
	waitID, _ := waiting.ProcessSnapshots()[0].WaitID()
	id, _ := ParseSignalID("signal:answer")
	answer, _ := NewSignalRequest(id, waitID, []byte(`{"kind":"answer","value":"approved"}`))
	if accepted, deliveryErr := process.DeliverSignals(t.Context(), answer); !accepted || deliveryErr != nil {
		t.Fatalf("answer = %t, %v", accepted, deliveryErr)
	}
	if result := awaitResult(t, process); result.Status() != StatusCompleted {
		t.Fatalf("result = %s", result.Status())
	}
	completed, err := engine.CaptureTree(t.Context(), process.ID())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		tree   TreeSnapshot
		mutate func(*processSnapshotWire)
	}{
		{
			name: "terminal wait remains open", tree: completed,
			mutate: func(wire *processSnapshotWire) { wire.Mailbox.Waits[0].Closed = false },
		},
		{
			name: "wait has no opening Signal", tree: waiting,
			mutate: func(wire *processSnapshotWire) {
				wire.Mailbox.Signals = nil
				wire.Mailbox.SignalCursor = 0
				wire.Usage.AcceptedSignals = 0
			},
		},
		{
			name: "answer precedes opening", tree: completed,
			mutate: func(wire *processSnapshotWire) {
				wire.Mailbox.Signals[0], wire.Mailbox.Signals[1] = wire.Mailbox.Signals[1], wire.Mailbox.Signals[0]
				wire.Mailbox.Signals[0].ArrivalSequence = 1
				wire.Mailbox.Signals[1].ArrivalSequence = 2
			},
		},
		{
			name: "unconsumed answer already closed its wait", tree: waiting,
			mutate: func(wire *processSnapshotWire) {
				signal, _ := answer.signal()
				wire.Mailbox.Signals = append(wire.Mailbox.Signals, signalRecordWire{ArrivalSequence: 2, Signal: signal})
				wire.Mailbox.Waits[0].Answered = true
				wire.Mailbox.Waits[0].Closed = true
				wire.Usage.AcceptedSignals++
				wire.Status = StatusPaused
				wire.PauseReason = "pending answer"
				wire.CurrentWaitID = nil
			},
		},
		{
			name: "unknown current wait", tree: waiting,
			mutate: func(wire *processSnapshotWire) {
				unknown, _ := ParseWaitID("wait:unknown")
				wire.CurrentWaitID = &unknown
			},
		},
		{
			name: "closed current wait", tree: waiting,
			mutate: func(wire *processSnapshotWire) { wire.Mailbox.Waits[0].Closed = true },
		},
		{
			name: "answered current wait", tree: waiting,
			mutate: func(wire *processSnapshotWire) {
				signal, _ := answer.signal()
				wire.Mailbox.Signals = append(wire.Mailbox.Signals, signalRecordWire{ArrivalSequence: 2, Signal: signal})
				wire.Mailbox.Waits[0].Answered = true
				wire.Usage.AcceptedSignals++
			},
		},
		{
			name: "multiple answers for one wait", tree: completed,
			mutate: func(wire *processSnapshotWire) {
				secondID, _ := ParseSignalID("signal:second-answer")
				request, _ := NewSignalRequest(secondID, waitID, answer.Payload())
				signal, _ := request.signal()
				wire.Mailbox.Signals = append(wire.Mailbox.Signals, signalRecordWire{ArrivalSequence: 3, Signal: signal})
				wire.Usage.AcceptedSignals++
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, parseErr := ParseTreeSnapshot(test.tree.JSON()); parseErr != nil {
				t.Fatalf("valid baseline: %v", parseErr)
			}
			wire, err := test.tree.ProcessSnapshots()[0].wire()
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(&wire)
			data, err := json.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			if _, parseErr := ParseProcessSnapshot(data); !errors.Is(parseErr, ErrInvalidSnapshot) {
				t.Errorf("Process parser = %v; want ErrInvalidSnapshot", parseErr)
			}
			var fields map[string]json.RawMessage
			if decodeErr := json.Unmarshal(test.tree.JSON(), &fields); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			fields["process_snapshots"], err = json.Marshal([]json.RawMessage{data})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if _, parseErr := ParseTreeSnapshot(encoded); !errors.Is(parseErr, ErrInvalidTreeSnapshot) || !errors.Is(parseErr, ErrInvalidSnapshot) {
				t.Errorf("Tree parser = %v; want ErrInvalidTreeSnapshot and ErrInvalidSnapshot", parseErr)
			}
		})
	}
}
