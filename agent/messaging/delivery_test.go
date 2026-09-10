package messaging_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
	"github.com/Tangerg/scope/agent/messaging"
)

func TestReplayReconcilesConsumedMessageAtOriginalRecipient(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := agenttest.NewMemoryTreeDurability()
		receiverEngine, err := agent.NewEngine(agent.EngineConfig{TreeDurability: store})
		if err != nil {
			t.Fatal(err)
		}
		receiver, err := receiverEngine.Start(t.Context(), bind(t, newGate(t), nil), input(t, "review this plan"))
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		waitID, waiting := inspect(t, receiverEngine, receiver).WaitID()
		if !waiting {
			t.Fatal("receiver did not wait")
		}
		port := &recipientPort{engine: receiverEngine, recipient: receiver, checkReceipts: true, firstAdmission: make(chan struct{}), release: make(chan struct{})}
		release := sync.OnceFunc(func() { close(port.release) })
		defer release()
		dispatcher, err := messaging.NewDispatcher(messaging.DispatcherConfig{Port: port})
		if err != nil {
			t.Fatal(err)
		}
		deployment := bind(t, newSender(t), dispatcher)
		senderEngine, err := agent.NewEngine(agent.EngineConfig{TreeDurability: store})
		if err != nil {
			t.Fatal(err)
		}
		sender, err := senderEngine.Start(t.Context(), deployment, input(t, messaging.Message{Recipient: receiver.ID(), WaitID: &waitID, Payload: input(t, "approved")}))
		if err != nil {
			t.Fatal(err)
		}
		<-port.firstAdmission
		received := finish(t, receiver)
		if received.Status() != agent.StatusCompleted || received.Usage() != (agent.Usage{CommittedSteps: 3, PreparedEffects: 1, AcceptedSignals: 2}) {
			t.Fatalf("receiver=%+v", received)
		}
		checkpoint, present, loadErr := store.LoadTree(t.Context(), sender.ID())
		if loadErr != nil || !present {
			t.Fatalf("sender checkpoint=%t %v", present, loadErr)
		}
		restoredEngine, err := agent.NewEngine(agent.EngineConfig{TreeDurability: store})
		if err != nil {
			t.Fatal(err)
		}
		restored, err := restoredEngine.RestoreTree(t.Context(), deployment, checkpoint)
		if err != nil {
			t.Fatal(err)
		}
		replayed := finish(t, restored)
		if replayed.Status() != agent.StatusCompleted || replayed.Usage() != (agent.Usage{CommittedSteps: 2, PreparedEffects: 1, AcceptedSignals: 1}) {
			t.Fatalf("sender replay=%+v", replayed)
		}
		port.mu.Lock()
		calls := append([]agent.SignalRequest(nil), port.calls...)
		port.mu.Unlock()
		if len(calls) != 2 || calls[0].ID() != calls[1].ID() {
			t.Fatalf("replay changed delivery identity: %v", calls)
		}
		for _, call := range calls {
			if got, _ := call.WaitID(); got != waitID || string(call.Payload()) != `"approved"` {
				t.Fatal("replay changed delivery content")
			}
		}
		receipts := inspect(t, receiverEngine, receiver).SignalReceipts()
		if len(receipts) != 2 || !receipts[1].Consumed() || !receipts[1].Matches(calls[0]) || receipts[1].ArrivalSequence() != 2 {
			t.Fatalf("receipt=%+v", receipts)
		}
		if _, pending := receipts[1].PendingSignal(); pending {
			t.Fatal("consumed payload was retained")
		}
		conflict, err := agent.NewSignalRequest(calls[0].ID(), waitID, []byte(`"different"`))
		if err != nil {
			t.Fatal(err)
		}
		if deliveryErr := port.Deliver(t.Context(), restored.ID(), receiver.ID(), conflict); !errors.Is(deliveryErr, agent.ErrSignalConflict) {
			t.Fatalf("conflict=%v", deliveryErr)
		}
		// A replacement gate never receives the unresolved original delivery.
		replacement, err := receiverEngine.Start(t.Context(), bind(t, newGate(t), nil), input(t, "next review"))
		if err != nil {
			t.Fatal(err)
		}
		if deliveryErr := port.Deliver(t.Context(), restored.ID(), replacement.ID(), calls[0]); !errors.Is(deliveryErr, agent.ErrSignalRejected) {
			t.Fatalf("retarget=%v", deliveryErr)
		}
		if cancelErr := replacement.RequestCancellation(t.Context(), "test complete"); cancelErr != nil {
			t.Fatal(cancelErr)
		}
		finish(t, replacement)
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

func TestLostDeliveryAcknowledgmentRemainsUnknownUntilAdjudicated(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		engine, err := agent.NewEngine(agent.EngineConfig{})
		if err != nil {
			t.Fatal(err)
		}
		receiver, err := engine.Start(t.Context(), bind(t, newGate(t), nil), input(t, "review"))
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		waitID, _ := inspect(t, engine, receiver).WaitID()
		port := &recipientPort{engine: engine, recipient: receiver, lostAcknowledgment: true}
		dispatcher, err := messaging.NewDispatcher(messaging.DispatcherConfig{Port: port})
		if err != nil {
			t.Fatal(err)
		}
		sender, err := engine.Start(t.Context(), bind(t, newSender(t), dispatcher), input(t, messaging.Message{Recipient: receiver.ID(), WaitID: &waitID, Payload: input(t, "revise")}))
		if err != nil {
			t.Fatal(err)
		}
		finish(t, receiver)
		synctest.Wait()
		unknown := inspect(t, engine, sender).UnknownEffectIDs()
		if len(unknown) != 1 || len(port.calls) != 1 {
			t.Fatalf("unknown=%v calls=%d", unknown, len(port.calls))
		}
		receipts := inspect(t, engine, receiver).SignalReceipts()
		if !receipts[len(receipts)-1].Matches(port.calls[0]) {
			t.Fatal("adjudication lacks admission evidence")
		}
		proof := input(t, messaging.Receipt{Recipient: receiver.ID(), SignalID: port.calls[0].ID()})
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
		engine, err := agent.NewEngine(agent.EngineConfig{})
		if err != nil {
			t.Fatal(err)
		}
		receiver, err := engine.Start(t.Context(), bind(t, newGate(t), nil), input(t, "closed review"))
		if err != nil {
			t.Fatal(err)
		}
		if cancelErr := receiver.RequestCancellation(t.Context(), "recipient closed"); cancelErr != nil {
			t.Fatal(cancelErr)
		}
		finish(t, receiver)
		port := &recipientPort{engine: engine, recipient: receiver, checkReceipts: true}
		dispatcher, err := messaging.NewDispatcher(messaging.DispatcherConfig{Port: port})
		if err != nil {
			t.Fatal(err)
		}
		sender, err := engine.Start(t.Context(), bind(t, newSender(t), dispatcher), input(t, messaging.Message{Recipient: receiver.ID(), Payload: input(t, "late review")}))
		if err != nil {
			t.Fatal(err)
		}
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
