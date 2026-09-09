package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestMailboxConsumptionDropsPayloadOnlyFromAdoptedCandidate(t *testing.T) {
	mailbox := newSignalMailbox()
	payload, err := json.Marshal(strings.Repeat("context", 10_000))
	if err != nil {
		t.Fatal(err)
	}
	signal := mustMailboxSignal(t, "signal:large", WaitID{}, payload)
	if accepted, enqueueErr := mailbox.enqueue(StatusRunning, signal, signalSourceExternal); enqueueErr != nil || !accepted {
		t.Fatalf("admission=%t error=%v", accepted, enqueueErr)
	}
	candidate := mailbox.clone()
	if _, commitErr := candidate.commit(1); commitErr != nil {
		t.Fatal(commitErr)
	}
	if pending := mailbox.pending(); len(pending) != 1 || !bytes.Equal(pending[0].Payload(), payload) {
		t.Fatal("candidate consumption changed the authoritative pending input")
	}
	encoded, err := json.Marshal(candidate.snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) >= 512 || bytes.Contains(encoded, []byte("context")) {
		t.Fatalf("consumed mailbox retained payload: %d bytes", len(encoded))
	}
	var wire mailboxWire
	if decodeErr := json.Unmarshal(encoded, &wire); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	restored, err := restoreSignalMailbox(wire, StatusRunning)
	if err != nil {
		t.Fatal(err)
	}
	if accepted, retryErr := restored.enqueue(StatusRunning, signal, signalSourceExternal); retryErr != nil || accepted {
		t.Fatalf("identical retry after compaction=%t error=%v", accepted, retryErr)
	}
}

func TestMailboxRestoreEnforcesPayloadConsumptionBoundary(t *testing.T) {
	signal := mustMailboxSignal(t, "signal:payload", WaitID{}, json.RawMessage(`{"value":"accepted"}`))
	for _, test := range []struct {
		name   string
		cursor uint64
		mutate func(*signalRecordWire)
	}{
		{name: "pending payload missing", mutate: func(record *signalRecordWire) { record.Payload = nil }},
		{name: "pending payload changed", mutate: func(record *signalRecordWire) { record.Payload = json.RawMessage(`{"value":"changed"}`) }},
		{name: "digest missing", mutate: func(record *signalRecordWire) { record.PayloadDigest = Digest{} }},
		{name: "consumed payload retained", cursor: 1, mutate: func(*signalRecordWire) {}},
	} {
		t.Run(test.name, func(t *testing.T) {
			record := mailboxRecordWire(1, signal)
			test.mutate(&record)
			if _, err := restoreSignalMailbox(mailboxWire{Signals: []signalRecordWire{record}, SignalCursor: test.cursor}, StatusRunning); err == nil {
				t.Fatal("restored an inconsistent payload retention boundary")
			}
		})
	}
}

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

func TestMailboxRejectsConflictingIdentityAfterConsumptionAndRestore(t *testing.T) {
	mailbox := newSignalMailbox()
	original := mustMailboxSignal(t, "signal:conflict", WaitID{}, json.RawMessage(`{"value":"original"}`))
	if accepted, err := mailbox.enqueue(StatusRunning, original, signalSourceExternal); !accepted || err != nil {
		t.Fatalf("initial admission=%t %v", accepted, err)
	}
	if _, err := mailbox.commit(1); err != nil {
		t.Fatal(err)
	}
	restored, err := restoreSignalMailbox(mailbox.snapshot(), StatusRunning)
	if err != nil {
		t.Fatal(err)
	}
	waitID, _ := ParseWaitID("wait:different")
	for _, signal := range []Signal{
		mustMailboxSignal(t, original.ID().String(), WaitID{}, json.RawMessage(`{"value":"changed"}`)),
		mustMailboxSignal(t, original.ID().String(), waitID, original.Payload()),
	} {
		if accepted, err := restored.enqueue(StatusRunning, signal, signalSourceExternal); accepted || !errors.Is(err, ErrSignalConflict) {
			t.Fatalf("conflicting admission=%t %v", accepted, err)
		}
	}
	if restored.arrivalSequence() != 1 || restored.committedSignalCursor() != 1 {
		t.Fatal("conflicting admission changed mailbox history")
	}
}

func TestMailboxRoutesWaitAnswersAndHandlesEarlyArrival(t *testing.T) {
	mailbox := newSignalMailbox()
	key, _ := ParseWaitKey("approval:1")
	waitID, _ := ParseWaitID("wait:1")
	opened := mustMailboxSignal(t, "signal:opened", waitID, json.RawMessage(`{}`))
	if err := mailbox.openWait(key, opened, true); err != nil {
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

func TestMailboxQueuesUnaddressedWaitingAndRejectsAddressedPausedSignals(t *testing.T) {
	mailbox := newSignalMailbox()
	unaddressed := mustMailboxSignal(t, "signal:plain", WaitID{}, json.RawMessage(`{}`))
	if accepted, err := mailbox.enqueue(StatusWaiting, unaddressed, signalSourceExternal); err != nil || !accepted {
		t.Fatalf("unaddressed Waiting admission=%t error=%v", accepted, err)
	}
	waitID, _ := ParseWaitID("wait:1")
	addressed := mustMailboxSignal(t, "signal:answer", waitID, json.RawMessage(`{}`))
	if _, err := mailbox.enqueue(StatusPaused, addressed, signalSourceExternal); !errors.Is(err, ErrSignalRejected) {
		t.Fatalf("addressed Paused error = %v", err)
	}
	paused := mustMailboxSignal(t, "signal:paused", WaitID{}, json.RawMessage(`{}`))
	if accepted, err := mailbox.enqueue(StatusPaused, paused, signalSourceExternal); err != nil || !accepted {
		t.Fatalf("unaddressed Paused enqueue = %t, %v", accepted, err)
	}
}

func TestMailboxSnapshotRestoresDeduplicationCursorAndWaitFacts(t *testing.T) {
	mailbox := newSignalMailbox()
	key, _ := ParseWaitKey("approval:1")
	waitID, _ := ParseWaitID("wait:1")
	opened := mustMailboxSignal(t, "signal:opened", waitID, json.RawMessage(`{}`))
	if err := mailbox.openWait(key, opened, true); err != nil {
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
	if _, err := mailbox.commit(2); err != nil {
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
	restored, err := restoreSignalMailbox(decoded, StatusRunning)
	if err != nil {
		t.Fatal(err)
	}
	if restored.committedSignalCursor() != 2 || len(restored.pending()) != 1 || restored.pending()[0].ID() != plain.ID() {
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
	opened := mustMailboxSignal(t, "signal:opened", waitID, json.RawMessage(`{"kind":"wait_opened"}`))
	if err := mailbox.openWait(key, opened, true); err != nil {
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
	opened := mustMailboxSignal(t, "signal:opened", waitID, json.RawMessage(`{}`))
	if err := mailbox.openWait(key, opened, false); err != nil {
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
		{Signals: []signalRecordWire{mailboxRecordWire(2, signal)}},
		{Signals: []signalRecordWire{mailboxRecordWire(1, signal), mailboxRecordWire(2, signal)}},
	} {
		if _, err := restoreSignalMailbox(wire, StatusRunning); err == nil {
			t.Fatalf("restoreSignalMailbox(%+v) unexpectedly succeeded", wire)
		}
	}
}

func TestMailboxRestoresWaitLifecycleAtEveryBoundary(t *testing.T) {
	for _, external := range []bool{true, false} {
		t.Run(strconv.FormatBool(external), func(t *testing.T) {
			mailbox := newSignalMailbox()
			key, _ := ParseWaitKey("reusable")
			source := signalSourceChildCompletion
			if external {
				source = signalSourceExternal
			}
			for index := range 3 {
				id, _ := ParseWaitID("wait:" + strconv.Itoa(index))
				opened := mustMailboxSignal(t, "signal:opened-"+strconv.Itoa(index), id, json.RawMessage(`{}`))
				if err := mailbox.openWait(key, opened, external); err != nil {
					t.Fatal(err)
				}
				mailbox = restoredMailbox(t, mailbox, StatusRunning)
				if shouldWait, err := mailbox.enterWait(id); err != nil || !shouldWait {
					t.Fatalf("unanswered wait=%t error=%v", shouldWait, err)
				}
				answer := mustMailboxSignal(t, "signal:answer-"+strconv.Itoa(index), id, json.RawMessage(`{}`))
				if accepted, err := mailbox.enqueue(StatusRunning, answer, source); err != nil || !accepted {
					t.Fatalf("answer accepted=%t error=%v", accepted, err)
				}
				mailbox = restoredMailbox(t, mailbox, StatusRunning)
				if accepted, err := mailbox.enqueue(StatusRunning, answer, source); err != nil || accepted {
					t.Fatalf("restored duplicate accepted=%t error=%v", accepted, err)
				}
				if index == 2 {
					mailbox.closeAllWaits()
					restoredMailbox(t, mailbox, StatusKilled)
					break
				}
				if _, err := mailbox.commit(1); err != nil {
					t.Fatal(err)
				}
				mailbox = restoredMailbox(t, mailbox, StatusRunning)
				if shouldWait, err := mailbox.enterWait(id); err != nil || shouldWait {
					t.Fatalf("early answer wait=%t error=%v", shouldWait, err)
				}
				if _, err := mailbox.commit(1); err != nil {
					t.Fatal(err)
				}
				mailbox = restoredMailbox(t, mailbox, StatusRunning)
			}
		})
	}
}

func restoredMailbox(t testing.TB, mailbox signalMailbox, status Status) signalMailbox {
	t.Helper()
	wire := mailbox.snapshot()
	restored, err := restoreSignalMailbox(wire, status)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(restored.snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("restored mailbox=%s, want %s", got, want)
	}
	return restored
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

func mailboxRecordWire(sequence uint64, signal Signal) signalRecordWire {
	record := newSignalRecord(signal, false)
	record.arrivalSequence = sequence
	return record.snapshot()
}
