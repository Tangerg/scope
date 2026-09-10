package agent

import (
	"errors"
	"testing"
)

func TestSignalReceiptsReconcileOnlyExternalAdmissions(t *testing.T) {
	for _, test := range []struct {
		name     string
		wait     bool
		external bool
	}{
		{name: "unaddressed", external: true},
		{name: "external wait", wait: true, external: true},
		{name: "child wait", wait: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			mailbox := newSignalMailbox()
			var waitID WaitID
			var opening Signal
			if test.wait {
				waitID = controlValue(ParseWaitID("wait:receipt"))
				opening = mustMailboxSignal(t, "signal:opening", waitID, []byte(`"opened"`))
				if err := mailbox.openWait(controlValue(ParseWaitKey("receipt")), opening, test.external); err != nil {
					t.Fatal(err)
				}
			}
			answer := mustMailboxSignal(t, "signal:answer", waitID, []byte(`{"value":"answer"}`))
			source := signalSourceExternal
			if !test.external {
				source = signalSourceChildWait
			}
			if accepted, err := mailbox.enqueue(StatusRunning, answer, source); !accepted || err != nil {
				t.Fatalf("admission = %t, %v", accepted, err)
			}
			for _, consumed := range []bool{false, true} {
				if consumed {
					if _, err := mailbox.commit(uint32(mailbox.pendingCount())); err != nil {
						t.Fatal(err)
					}
				}
				mailbox = restoredMailbox(t, mailbox, StatusRunning)
				receipts := snapshotSignalReceipts(mailbox.snapshot())
				if test.wait && receipts[0].Matches(SignalRequest(opening)) {
					t.Errorf("consumed=%t: opening receipt proved an external admission", consumed)
				}
				receipt := receipts[len(receipts)-1]
				if receipt.Consumed() != consumed || receipt.Matches(SignalRequest(answer)) != test.external {
					t.Errorf("consumed=%t: answer receipt = %+v, matches=%t", consumed, receipt, receipt.Matches(SignalRequest(answer)))
				}
				if _, present := receipt.PendingSignal(); present == consumed {
					t.Errorf("consumed=%t: pending signal presence=%t", consumed, present)
				}
				if accepted, err := mailbox.enqueue(StatusRunning, answer, signalSourceExternal); accepted ||
					test.external && err != nil || !test.external && !errors.Is(err, ErrSignalRejected) {
					t.Errorf("consumed=%t: external replay = %t, %v", consumed, accepted, err)
				}
				if accepted, err := mailbox.enqueue(StatusRunning, answer, source); accepted || err != nil {
					t.Errorf("consumed=%t: authorized replay = %t, %v", consumed, accepted, err)
				}
			}
		})
	}
}
