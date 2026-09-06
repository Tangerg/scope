package agent

import (
	"encoding/json"
	"errors"
	"strconv"
	"testing"
)

func TestSignalRequestAndMailboxOwnPayloadAndDeduplicate(t *testing.T) {
	id, _ := ParseSignalID("signal:1")
	payload := json.RawMessage(` { "kind": "steer" } `)
	request, err := NewSignalRequest(id, WaitID{}, payload)
	if err != nil {
		t.Fatal(err)
	}
	payload[3] = 'x'
	signal, err := request.signal()
	if err != nil {
		t.Fatal(err)
	}
	mailbox := newSignalMailbox()
	accepted, err := mailbox.enqueue(StatusRunning, signal, signalSourceExternal)
	if err != nil || !accepted {
		t.Fatalf("first enqueue = %t, %v", accepted, err)
	}
	accepted, err = mailbox.enqueue(StatusRunning, signal, signalSourceExternal)
	if err != nil || accepted {
		t.Fatalf("duplicate enqueue = %t, %v", accepted, err)
	}
	if mailbox.arrivalSequence() != 1 || len(mailbox.pending()) != 1 || string(mailbox.pending()[0].Payload()) != `{"kind":"steer"}` {
		t.Fatalf("mailbox = sequence %d pending %+v", mailbox.arrivalSequence(), mailbox.pending())
	}
}

func TestMailboxCommitsOnlyAnExplicitSignalPrefix(t *testing.T) {
	mailbox := newSignalMailbox()
	for index := 1; index <= 3; index++ {
		id, _ := ParseSignalID("signal:" + strconv.Itoa(index))
		signal := mustMailboxSignal(t, id.String(), WaitID{}, json.RawMessage(`{"kind":"input"}`))
		if _, err := mailbox.enqueue(StatusRunning, signal, signalSourceExternal); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := mailbox.commit(2); err != nil {
		t.Fatal(err)
	}
	if mailbox.committedSignalCursor() != 2 || len(mailbox.pending()) != 1 || mailbox.pending()[0].ID().String() != "signal:3" {
		t.Fatalf("mailbox cursor = %d pending %+v", mailbox.committedSignalCursor(), mailbox.pending())
	}
	if _, err := mailbox.commit(2); !errors.Is(err, errMailboxCursor) {
		t.Fatalf("over-consume error = %v, want errMailboxCursor", err)
	}
	if mailbox.committedSignalCursor() != 2 {
		t.Fatal("failed cursor commit changed the authoritative cursor")
	}
}

func TestMailboxRoutesWaitAnswersAndHandlesEarlyArrival(t *testing.T) {
	mailbox := newSignalMailbox()
	key, _ := ParseWaitKey("approval:1")
	waitID, _ := ParseWaitID("wait:1")
	if err := mailbox.registerWait(key, waitID, true); err != nil {
		t.Fatal(err)
	}
	answerID, _ := ParseSignalID("signal:answer")
	request, err := NewSignalRequest(answerID, waitID, json.RawMessage(`{"approved":true}`))
	if err != nil {
		t.Fatal(err)
	}
	answer, err := request.signal()
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := mailbox.enqueue(StatusRunning, answer, signalSourceExternal)
	if err != nil || !accepted {
		t.Fatalf("early answer enqueue = %t, %v", accepted, err)
	}
	shouldWait, err := mailbox.enterWait(waitID)
	if err != nil || shouldWait {
		t.Fatalf("enter answered wait = %t, %v", shouldWait, err)
	}

	secondID, _ := ParseSignalID("signal:second-answer")
	second := mustMailboxSignal(t, secondID.String(), waitID, json.RawMessage(`{"approved":false}`))
	if _, err := mailbox.enqueue(StatusWaiting, second, signalSourceExternal); !errors.Is(err, ErrSignalRejected) {
		t.Fatalf("second answer error = %v, want ErrSignalRejected", err)
	}
	if err := mailbox.closeWait(waitID); err != nil {
		t.Fatal(err)
	}
	if _, err := mailbox.enqueue(StatusWaiting, second, signalSourceExternal); !errors.Is(err, ErrSignalRejected) {
		t.Fatalf("closed wait answer error = %v, want ErrSignalRejected", err)
	}
}

func TestMailboxRejectsUnaddressedWaitingAndAddressedPausedSignals(t *testing.T) {
	mailbox := newSignalMailbox()
	unaddressed := mustMailboxSignal(t, "signal:plain", WaitID{}, json.RawMessage(`{}`))
	if _, err := mailbox.enqueue(StatusWaiting, unaddressed, signalSourceExternal); !errors.Is(err, ErrSignalRejected) {
		t.Fatalf("unaddressed Waiting error = %v", err)
	}
	waitID, _ := ParseWaitID("wait:1")
	addressed := mustMailboxSignal(t, "signal:answer", waitID, json.RawMessage(`{}`))
	if _, err := mailbox.enqueue(StatusPaused, addressed, signalSourceExternal); !errors.Is(err, ErrSignalRejected) {
		t.Fatalf("addressed Paused error = %v", err)
	}
	if accepted, err := mailbox.enqueue(StatusPaused, unaddressed, signalSourceExternal); err != nil || !accepted {
		t.Fatalf("unaddressed Paused enqueue = %t, %v", accepted, err)
	}
}

func TestMailboxSnapshotRestoresDeduplicationCursorAndWaitFacts(t *testing.T) {
	mailbox := newSignalMailbox()
	key, _ := ParseWaitKey("approval:1")
	waitID, _ := ParseWaitID("wait:1")
	if err := mailbox.registerWait(key, waitID, true); err != nil {
		t.Fatal(err)
	}
	answer := mustMailboxSignal(t, "signal:answer", waitID, json.RawMessage(`{"approved":true}`))
	if _, err := mailbox.enqueue(StatusRunning, answer, signalSourceExternal); err != nil {
		t.Fatal(err)
	}
	plain := mustMailboxSignal(t, "signal:plain", WaitID{}, json.RawMessage(`{"kind":"steer"}`))
	if _, err := mailbox.enqueue(StatusRunning, plain, signalSourceExternal); err != nil {
		t.Fatal(err)
	}
	if _, err := mailbox.commit(1); err != nil {
		t.Fatal(err)
	}

	wire := mailbox.snapshot()
	data, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var decoded mailboxWire
	if unmarshalErr := json.Unmarshal(data, &decoded); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	restored, err := restoreSignalMailbox(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if restored.committedSignalCursor() != 1 || len(restored.pending()) != 1 || restored.pending()[0].ID() != plain.ID() {
		t.Fatalf("restored mailbox cursor=%d pending=%+v", restored.committedSignalCursor(), restored.pending())
	}
	if accepted, err := restored.enqueue(StatusRunning, answer, signalSourceExternal); err != nil || accepted {
		t.Fatalf("restored duplicate enqueue = %t, %v", accepted, err)
	}
	if _, err := restored.enterWait(waitID); !errors.Is(err, errWaitState) {
		t.Fatalf("restored consumed answer error = %v, want errWaitState", err)
	}
}

func TestMailboxWaitOpenedSignalDoesNotAnswerOrCloseWait(t *testing.T) {
	mailbox := newSignalMailbox()
	key, _ := ParseWaitKey("approval:1")
	waitID, _ := ParseWaitID("wait:1")
	if err := mailbox.registerWait(key, waitID, true); err != nil {
		t.Fatal(err)
	}
	opened := mustMailboxSignal(t, "signal:opened", waitID, json.RawMessage(`{"kind":"wait_opened"}`))
	if err := mailbox.enqueueWaitOpened(opened); err != nil {
		t.Fatal(err)
	}
	if _, err := mailbox.commit(1); err != nil {
		t.Fatal(err)
	}
	if shouldWait, err := mailbox.enterWait(waitID); err != nil || !shouldWait {
		t.Fatalf("enter open wait = %t, %v", shouldWait, err)
	}
}

func TestMailboxCommitReportsOnlyConsumedChildWaits(t *testing.T) {
	mailbox := newSignalMailbox()
	key, _ := ParseWaitKey("children")
	waitID, _ := ParseWaitID("wait:children")
	if err := mailbox.registerWait(key, waitID, false); err != nil {
		t.Fatal(err)
	}
	opened := mustMailboxSignal(t, "signal:opened", waitID, json.RawMessage(`{}`))
	if err := mailbox.enqueueWaitOpened(opened); err != nil {
		t.Fatal(err)
	}
	answer := mustMailboxSignal(t, "signal:answer", waitID, json.RawMessage(`{}`))
	if _, err := mailbox.enqueue(StatusRunning, answer, signalSourceChildCompletion); err != nil {
		t.Fatal(err)
	}
	candidate := mailbox.clone()
	if closed, err := candidate.commit(1); err != nil || len(closed) != 0 {
		t.Fatalf("consume wait-opened signal = %v, %v; want no closed child waits", closed, err)
	}
	closed, err := candidate.commit(1)
	if err != nil || len(closed) != 1 || closed[0] != waitID {
		t.Fatalf("consume child answer = %v, %v; want %s", closed, err, waitID)
	}
	if _, err := candidate.enterWait(waitID); !errors.Is(err, errWaitState) {
		t.Fatalf("consumed child wait error = %v, want errWaitState", err)
	}
	if _, err := mailbox.enterWait(waitID); err != nil {
		t.Fatalf("candidate commit closed the authoritative wait: %v", err)
	}
	if mailbox.committedSignalCursor() != 0 || candidate.committedSignalCursor() != 2 {
		t.Fatalf("signal cursors = %d, %d; want 0, 2", mailbox.committedSignalCursor(), candidate.committedSignalCursor())
	}
}

func TestMailboxRestoreRejectsInvalidWire(t *testing.T) {
	signal := mustMailboxSignal(t, "signal:1", WaitID{}, json.RawMessage(`{}`))
	for _, wire := range []mailboxWire{
		{SignalCursor: 1},
		{Signals: []signalRecordWire{{ArrivalSequence: 2, Signal: signal}}},
		{Signals: []signalRecordWire{{ArrivalSequence: 1, Signal: signal}, {ArrivalSequence: 2, Signal: signal}}},
	} {
		if _, err := restoreSignalMailbox(wire); err == nil {
			t.Fatalf("restoreSignalMailbox(%+v) unexpectedly succeeded", wire)
		}
	}
}

func mustMailboxSignal(t testing.TB, value string, waitID WaitID, payload json.RawMessage) Signal {
	t.Helper()
	signalID, err := ParseSignalID(value)
	if err != nil {
		t.Fatal(err)
	}
	signal, err := newSignal(signalID, waitID, payload)
	if err != nil {
		t.Fatal(err)
	}
	return signal
}
