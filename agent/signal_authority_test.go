package agent

import (
	"github.com/Tangerg/scope/agent/internal/jsonwire"

	jsonv2 "encoding/json/v2"
	"errors"
	"testing"
)

func TestExternalDeliveryCannotClaimEngineSignalIdentity(t *testing.T) {
	process := admissionTestProcess(t, 0)
	effectID := process.handle.processID().effectID(1, 0)
	for _, id := range []SignalID{effectID.settlementSignalID(), effectID.waitID().childWaitSignalID()} {
		t.Run(id.String(), func(t *testing.T) {
			signal := controlValue(NewSignal(id, WaitID{}, []byte(`"forged"`)))
			if accepted, err := admitTestSignals(process, admissionTestLimits(), []Signal{signal}, signalSourceExternal); accepted || !errors.Is(err, ErrSignalRejected) {
				t.Errorf("external admission claimed Engine identity: %t, %v", accepted, err)
			}
			if _, err := NewSignalRequest(id, WaitID{}, signal.Payload()); !errors.Is(err, ErrInvalidSignalRequest) {
				t.Errorf("request constructor accepted Engine identity: %v", err)
			}
			encoded, err := jsonv2.Marshal(signal)
			if err != nil {
				t.Fatal(err)
			}
			var request SignalRequest
			if err := jsonv2.Unmarshal(encoded, &request); !errors.Is(err, ErrInvalidSignalRequest) {
				t.Errorf("request decoding accepted Engine identity: %v", err)
			}
		})
	}
	if process.usage().AcceptedSignals != 0 {
		t.Fatal("rejected delivery changed mailbox")
	}
}

func TestSignalAuthorityFollowsIdentityFacts(t *testing.T) {
	waitID := controlValue(ParseWaitID("wait:authority"))
	for _, test := range []struct {
		id      string
		waitID  WaitID
		source  signalSource
		allowed bool
	}{
		{id: "signal:caller", source: signalSourceExternal, allowed: true},
		{id: "signal:caller", waitID: waitID, source: signalSourceExternal, allowed: true},
		{id: "signal:engine:settled", source: signalSourceSettlement, allowed: true},
		{id: "signal:engine:answer", waitID: waitID, source: signalSourceChildWait, allowed: true},
		{id: "signal:engine:reserved", source: signalSourceExternal},
		{id: "signal:caller", source: signalSourceSettlement},
		{id: "signal:engine:answer", waitID: waitID, source: signalSourceSettlement},
		{id: "signal:engine:settled", source: signalSourceChildWait},
	} {
		signal := mustMailboxSignal(t, test.id, test.waitID, []byte(`null`))
		if _, err := newAdmissionRecord(signal, test.source); (err == nil) != test.allowed {
			t.Errorf("%s addressed=%t through %s: error=%v", test.id, test.waitID.Valid(), test.source, err)
		}
	}
	opening := newSignalRecord(mustMailboxSignal(t, "signal:caller", waitID, []byte(`null`))).wire()
	opening.Opens = &waitOpeningWire{Key: new(controlValue(ParseWaitKey("authority")))}
	if _, err := restoreSignalMailbox(mailboxWire{Signals: []signalRecordWire{opening}}); err == nil {
		t.Fatal("restoration accepted a Host identity opening a wait")
	}
	stored := []byte(`{"signals":[{"id":"signal:caller","payload_digest":"` + ComputeDigest([]byte(`null`)).String() + `","payload":null,"source":"external"}],"signal_cursor":0}`)
	if _, err := jsonwire.Decode[mailboxWire](stored); err == nil {
		t.Fatal("mailbox decoding accepted a stored copy of the signal source")
	}
}
