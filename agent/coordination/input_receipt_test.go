package coordination_test

import (
	startrand "crypto/rand"

	"errors"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/Tangerg/scope/agent/run"
)

func TestInputGateRejectsUnaddressedInputAfterFinalSignalWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		probe := &heldStepDefinition{
			Definition: inputGate(t), entered: make(chan run.Signal, 1), release: make(chan struct{}),
			matches: func(signal run.Signal) bool { return string(signal.Payload()) == `"answer"` },
		}
		release := sync.OnceFunc(func() { close(probe.release) })
		defer release()
		deployment := bind(t, probe, nil)
		store := run.NewMemoryStore()
		engine, err := run.New(t.Context(), run.Config{Store: store})
		if err != nil {
			t.Fatal(err)
		}
		process, err := engine.Start(t.Context(), deployment, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: encodedInput(t, "request")})
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		waitID, _ := inspect(t, engine, process).Snapshot.WaitID()
		openingReceipt := inspect(t, engine, process).Snapshot.SignalReceipts()[0]
		if _, requestErr := run.NewSignalRequest(openingReceipt.ID(), waitID, []byte(`"request"`)); !errors.Is(requestErr, run.ErrInvalidSignalRequest) {
			t.Fatalf("opening identity accepted as external request: %v", requestErr)
		}
		answerID, err := run.ParseSignalID("signal:answer")
		if err != nil {
			t.Fatal(err)
		}
		answer, err := run.NewSignalRequest(answerID, waitID, []byte(`"answer"`))
		if err != nil {
			t.Fatal(err)
		}
		if accepted, deliveryErr := process.DeliverSignals(t.Context(), answer); deliveryErr != nil || !accepted {
			t.Fatalf("answer=%t %v", accepted, deliveryErr)
		}
		<-probe.entered
		lateID, err := run.ParseSignalID("signal:after-final-window")
		if err != nil {
			t.Fatal(err)
		}
		late, err := run.NewSignalRequest(lateID, run.WaitID{}, []byte(` { "follow_up": "retain me" } `))
		if err != nil {
			t.Fatal(err)
		}
		if accepted, deliveryErr := process.DeliverSignals(t.Context(), late); accepted || !errors.Is(deliveryErr, run.ErrSignalRejected) {
			t.Fatalf("late input=%t %v", accepted, deliveryErr)
		}
		before := inspect(t, engine, process).Snapshot.SignalReceipts()
		if len(before) != 2 || before[1].Consumed() {
			t.Fatalf("candidate claimed committed consumption: %+v", before)
		}
		release()
		completedOutput[run.Signal](t, process)
		if joinErr := process.Join(t.Context()); joinErr != nil {
			t.Fatal(joinErr)
		}
		head, present, loadErr := store.LoadTree(t.Context(), process.Relation().ProcessID())
		if loadErr != nil || !present {
			t.Fatalf("terminal head=%t %v", present, loadErr)
		}
		restoredEngine, err := run.New(t.Context(), run.Config{Store: store})
		if err != nil {
			t.Fatal(err)
		}
		restored, err := restoredEngine.Recover(t.Context(), deployment, head)
		if err != nil {
			t.Fatal(err)
		}
		if joinErr := restored.Join(t.Context()); joinErr != nil {
			t.Fatal(joinErr)
		}
		for _, snapshot := range []run.ProcessSnapshot{inspect(t, engine, process).Snapshot, inspect(t, restoredEngine, restored).Snapshot} {
			receipts := snapshot.SignalReceipts()
			if len(receipts) != 2 || !receipts[0].Consumed() || !receipts[1].Consumed() {
				t.Fatalf("terminal receipt facts=%+v", receipts)
			}
			if receipts[0].Matches(answer) || receipts[0].Matches(late) {
				t.Fatal("wait opening proved an external delivery")
			}
			if !receipts[1].Matches(answer) || receipts[1].Matches(late) {
				t.Fatal("terminal receipts changed immutable identity")
			}
			if got, addressed := receipts[1].WaitID(); !addressed || got != waitID {
				t.Fatal("consumed answer lost its wait")
			}
			if snapshot.Usage() != (run.Usage{CommittedSteps: 3, PreparedEffects: 1, AcceptedSignals: 2}) {
				t.Fatalf("late input accounting=%+v", snapshot.Usage())
			}
		}
		closeEngine(t, restoredEngine)
		closeEngine(t, engine)
	})
}
