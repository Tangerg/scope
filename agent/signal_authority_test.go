package agent

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestExternalDeliveryCannotClaimEngineSignalIdentity(t *testing.T) {
	process := admissionTestProcess(t, 0)
	effectID := process.handle.processID.effectID(1, 0)
	for _, id := range []SignalID{effectID.settlementSignalID(), effectID.waitID().childWaitSignalID()} {
		t.Run(id.String(), func(t *testing.T) {
			signal := controlValue(newSignal(id, WaitID{}, []byte(`"forged"`)))
			if accepted, err := process.admitSignals([]Signal{signal}, signalSourceExternal); accepted || !errors.Is(err, ErrSignalRejected) {
				t.Errorf("external admission claimed Engine identity: %t, %v", accepted, err)
			}
			if _, err := NewSignalRequest(id, WaitID{}, signal.Payload()); !errors.Is(err, ErrInvalidSignalRequest) {
				t.Errorf("request constructor accepted Engine identity: %v", err)
			}
			encoded, err := json.Marshal(signal)
			if err != nil {
				t.Fatal(err)
			}
			var request SignalRequest
			if err := json.Unmarshal(encoded, &request); !errors.Is(err, ErrInvalidSignalRequest) {
				t.Errorf("request decoding accepted Engine identity: %v", err)
			}
		})
	}
	if process.usage().AcceptedSignals != 0 {
		t.Fatal("rejected delivery changed mailbox")
	}
}

func TestMailboxRestoreRejectsSignalAuthorityMismatch(t *testing.T) {
	for _, source := range []signalSource{signalSourceExternal, signalSourceSettlement, signalSourceChildWait} {
		t.Run(string(source), func(t *testing.T) {
			id := "signal:caller"
			if source == signalSourceExternal {
				id = "signal:engine:reserved"
			}
			record := newSignalRecord(mustMailboxSignal(t, id, WaitID{}, []byte(`null`)), false)
			record.source = source
			record.arrivalSequence = 1
			wire := mailboxWire{Signals: []signalRecordWire{record.wire()}}
			if _, err := restoreSignalMailbox(wire, StatusRunning); err == nil {
				t.Fatal("restoration accepted conflicting identity authority")
			}
		})
	}
}
