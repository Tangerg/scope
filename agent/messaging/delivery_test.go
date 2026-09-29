package messaging_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/messaging"
)

func TestReplayReconcilesConsumedMessageAtOriginalRecipient(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := agent.NewMemoryTreeCommitter()
		receiverEngine := newEngine(t, store)
		receiver := start(t, receiverEngine, bind(t, newGate(t), nil), input(t, "review this plan"))
		synctest.Wait()
		waitID, waiting := inspect(t, receiverEngine, receiver).WaitID()
		if !waiting {
			t.Fatal("receiver did not wait")
		}
		port := &recipientPort{engine: receiverEngine, recipient: receiver, checkReceipts: true, firstAdmission: make(chan struct{}), release: make(chan struct{})}
		release := sync.OnceFunc(func() { close(port.release) })
		defer release()
		deployment := bind(t, newSender(t), newDispatcher(t, port))
		senderEngine := newEngine(t, store)
		sender := start(t, senderEngine, deployment, input(t, messaging.Message{Recipient: receiver.ID(), WaitID: &waitID, Payload: input(t, "approved")}))
		<-port.firstAdmission
		received := finish(t, receiver)
		if received.Status() != agent.StatusCompleted || received.Usage() != (agent.Usage{CommittedSteps: 3, PreparedEffects: 1, AcceptedSignals: 2}) {
			t.Fatalf("receiver=%+v", received)
		}
		checkpoint, present, loadErr := store.LoadTree(t.Context(), sender.ID())
		if loadErr != nil || !present {
			t.Fatalf("sender checkpoint=%t %v", present, loadErr)
		}
		restoredEngine := newEngine(t, store)
		restored, err := restoredEngine.RestoreTree(t.Context(), deployment, checkpoint)
		if err != nil {
			t.Fatal(err)
		}
		replayed := finish(t, restored)
		if replayed.Status() != agent.StatusCompleted || replayed.Usage() != (agent.Usage{CommittedSteps: 2, PreparedEffects: 1, AcceptedSignals: 1}) {
			t.Fatalf("sender replay=%+v", replayed)
		}
		original := assertReplayKeptDelivery(t, port.recordedCalls(), waitID)
		assertConsumedReceipt(t, inspect(t, receiverEngine, receiver).SignalReceipts(), original)
		assertPortKeepsOriginalAddress(t, port, receiverEngine, restored.ID(), original)
		release()
		if _, staleErr := sender.Await(t.Context()); !errors.Is(staleErr, agent.ErrTreeIncarnationConflict) {
			t.Fatalf("retired writer=%v", staleErr)
		}
		if joinErr := sender.Join(t.Context()); !errors.Is(joinErr, agent.ErrTreeIncarnationConflict) {
			t.Fatalf("retired drain=%v", joinErr)
		}
		closeEngine(t, senderEngine)
		closeEngine(t, restoredEngine)
		closeEngine(t, receiverEngine)
	})
}

func assertReplayKeptDelivery(t *testing.T, calls []agent.SignalRequest, waitID agent.WaitID) agent.SignalRequest {
	t.Helper()
	if len(calls) != 2 || calls[0].ID() != calls[1].ID() {
		t.Fatalf("replay changed delivery identity: %v", calls)
	}
	derived, ok := strings.CutPrefix(calls[0].ID().String(), "signal:message:")
	if _, parseErr := agent.ParseDigest(derived); !ok || parseErr != nil {
		t.Fatalf("invalid message identity=%s", calls[0].ID())
	}
	for _, call := range calls {
		if got, _ := call.WaitID(); got != waitID || string(call.Payload()) != `"approved"` {
			t.Fatal("replay changed delivery content")
		}
	}
	return calls[0]
}

func assertConsumedReceipt(t *testing.T, receipts []agent.SignalReceipt, delivered agent.SignalRequest) {
	t.Helper()
	if len(receipts) != 2 || !receipts[1].Consumed() || !receipts[1].Matches(delivered) || receipts[1].ArrivalSequence() != 2 {
		t.Fatalf("receipt=%+v", receipts)
	}
	if _, pending := receipts[1].PendingSignal(); pending {
		t.Fatal("consumed payload was retained")
	}
}

func assertPortKeepsOriginalAddress(
	t *testing.T,
	port *recipientPort,
	engine *agent.Engine,
	sender agent.ProcessID,
	original agent.SignalRequest,
) {
	t.Helper()
	waitID, _ := original.WaitID()
	conflict, err := agent.NewSignalRequest(original.ID(), waitID, []byte(`"different"`))
	if err != nil {
		t.Fatal(err)
	}
	if deliveryErr := port.Deliver(t.Context(), sender, port.recipient.ID(), conflict); !errors.Is(deliveryErr, agent.ErrSignalConflict) {
		t.Fatalf("conflict=%v", deliveryErr)
	}
	replacement := start(t, engine, bind(t, newGate(t), nil), input(t, "next review"))
	if deliveryErr := port.Deliver(t.Context(), sender, replacement.ID(), original); !errors.Is(deliveryErr, agent.ErrSignalRejected) {
		t.Fatalf("retarget=%v", deliveryErr)
	}
	if cancelErr := replacement.RequestCancellation(t.Context(), "test complete"); cancelErr != nil {
		t.Fatal(cancelErr)
	}
	finish(t, replacement)
}

func TestLostDeliveryAcknowledgmentRemainsUnknownUntilAdjudicated(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		engine := newEngine(t, agent.NewMemoryTreeCommitter())
		receiver := start(t, engine, bind(t, newGate(t), nil), input(t, "review"))
		synctest.Wait()
		waitID, _ := inspect(t, engine, receiver).WaitID()
		port := &recipientPort{engine: engine, recipient: receiver, lostAcknowledgment: true}
		sender := start(t, engine, bind(t, newSender(t), newDispatcher(t, port)), input(t, messaging.Message{Recipient: receiver.ID(), WaitID: &waitID, Payload: input(t, "revise")}))
		finish(t, receiver)
		synctest.Wait()
		unknown := inspect(t, engine, sender).UnknownEffectIDs()
		calls := port.recordedCalls()
		if len(unknown) != 1 || len(calls) != 1 {
			t.Fatalf("unknown=%v calls=%d", unknown, len(calls))
		}
		receipts := inspect(t, engine, receiver).SignalReceipts()
		if !receipts[len(receipts)-1].Matches(calls[0]) {
			t.Fatal("adjudication lacks admission evidence")
		}
		proof := input(t, messaging.Receipt{Recipient: receiver.ID(), SignalID: calls[0].ID()})
		settlement, err := agent.NewSettlement(unknown[0], agent.SettlementStatusSucceeded, proof.JSON())
		if err != nil {
			t.Fatal(err)
		}
		if resolveErr := sender.ResolveUnknownEffect(t.Context(), settlement); resolveErr != nil {
			t.Fatal(resolveErr)
		}
		if result := finish(t, sender); result.Status() != agent.StatusCompleted {
			t.Fatalf("resolved=%s", result.Status())
		}
		closeEngine(t, engine)
	})
}

func TestTerminalRecipientWithoutAdmissionEvidenceRemainsUnknown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		engine := newEngine(t, agent.NewMemoryTreeCommitter())
		receiver := start(t, engine, bind(t, newGate(t), nil), input(t, "closed review"))
		if cancelErr := receiver.RequestCancellation(t.Context(), "recipient closed"); cancelErr != nil {
			t.Fatal(cancelErr)
		}
		finish(t, receiver)
		port := &recipientPort{engine: engine, recipient: receiver, checkReceipts: true}
		sender := start(t, engine, bind(t, newSender(t), newDispatcher(t, port)), input(t, messaging.Message{Recipient: receiver.ID(), Payload: input(t, "late review")}))
		synctest.Wait()
		if unknown := inspect(t, engine, sender).UnknownEffectIDs(); len(unknown) != 1 {
			t.Fatalf("terminal delivery=%v", unknown)
		}
		if cancelErr := sender.RequestCancellation(context.Background(), "retain unresolved delivery"); cancelErr != nil {
			t.Fatal(cancelErr)
		}
		if result := finish(t, sender); result.Status() != agent.StatusCanceled || len(result.Termination().UnresolvedEffectIDs()) != 1 {
			t.Fatalf("terminated=%+v", result)
		}
		closeEngine(t, engine)
	})
}
