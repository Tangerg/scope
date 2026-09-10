package coordination_test

import (
	"sync"
	"testing"
	"testing/synctest"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
)

func TestSignalReceiptsRetainInputAfterFinalSignalWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		probe := &heldStepDefinition{
			Definition: inputGate(t), entered: make(chan agent.Signal, 1), release: make(chan struct{}),
			matches: func(signal agent.Signal) bool { return string(signal.Payload()) == `"answer"` },
		}
		release := sync.OnceFunc(func() { close(probe.release) })
		defer release()
		deployment := bind(t, probe, nil)
		store := agenttest.NewMemoryTreeDurability()
		engine, err := agent.NewEngine(agent.EngineConfig{TreeDurability: store})
		if err != nil {
			t.Fatal(err)
		}
		process, err := engine.Start(t.Context(), deployment, encodedInput(t, "request"))
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		waitID, _ := inspect(t, engine, process).Snapshot.WaitID()
		openingReceipt := inspect(t, engine, process).Snapshot.SignalReceipts()[0]
		openingReplay, err := agent.NewSignalRequest(openingReceipt.ID(), waitID, []byte(`"request"`))
		if err != nil {
			t.Fatal(err)
		}
		answerID, err := agent.ParseSignalID("signal:answer")
		if err != nil {
			t.Fatal(err)
		}
		answer, err := agent.NewSignalRequest(answerID, waitID, []byte(`"answer"`))
		if err != nil {
			t.Fatal(err)
		}
		if accepted, deliveryErr := process.DeliverSignals(t.Context(), answer); deliveryErr != nil || !accepted {
			t.Fatalf("answer=%t %v", accepted, deliveryErr)
		}
		<-probe.entered
		lateID, err := agent.ParseSignalID("signal:after-final-window")
		if err != nil {
			t.Fatal(err)
		}
		late, err := agent.NewSignalRequest(lateID, agent.WaitID{}, []byte(` { "follow_up": "retain me" } `))
		if err != nil {
			t.Fatal(err)
		}
		if accepted, deliveryErr := process.DeliverSignals(t.Context(), late); deliveryErr != nil || !accepted {
			t.Fatalf("late input=%t %v", accepted, deliveryErr)
		}
		before := inspect(t, engine, process).Snapshot.SignalReceipts()
		if len(before) != 3 || before[1].Consumed() || before[2].Consumed() {
			t.Fatalf("candidate claimed committed consumption: %+v", before)
		}
		release()
		completedOutput[agent.Signal](t, process)
		if joinErr := process.Join(t.Context()); joinErr != nil {
			t.Fatal(joinErr)
		}
		head, present, loadErr := store.LoadTree(t.Context(), process.ID())
		if loadErr != nil || !present {
			t.Fatalf("terminal head=%t %v", present, loadErr)
		}
		restoredEngine, err := agent.NewEngine(agent.EngineConfig{TreeDurability: store})
		if err != nil {
			t.Fatal(err)
		}
		restored, err := restoredEngine.RestoreTree(t.Context(), deployment, head)
		if err != nil {
			t.Fatal(err)
		}
		if joinErr := restored.Join(t.Context()); joinErr != nil {
			t.Fatal(joinErr)
		}
		for _, snapshot := range []agent.ProcessSnapshot{inspect(t, engine, process).Snapshot, inspect(t, restoredEngine, restored).Snapshot} {
			receipts := snapshot.SignalReceipts()
			if len(receipts) != 3 || !receipts[0].Consumed() || !receipts[1].Consumed() || receipts[2].Consumed() {
				t.Fatalf("terminal receipt facts=%+v", receipts)
			}
			if receipts[0].Matches(openingReplay) {
				t.Fatal("wait opening proved an external delivery")
			}
			if !receipts[1].Matches(answer) || !receipts[2].Matches(late) || receipts[2].PayloadDigest() != agent.ComputeDigest(late.Payload()) {
				t.Fatal("terminal receipts changed immutable identity")
			}
			pending, retained := receipts[2].PendingSignal()
			if !retained || pending.ID() != lateID || string(pending.Payload()) != `{"follow_up":"retain me"}` {
				t.Fatalf("late input was lost: %s", pending.Payload())
			}
			if _, addressed := receipts[2].WaitID(); addressed {
				t.Fatal("unaddressed input gained a wait")
			}
			if got, addressed := receipts[1].WaitID(); !addressed || got != waitID {
				t.Fatal("consumed answer lost its wait")
			}
			bytes := pending.Payload()
			bytes[0] = 'x'
			receipts[2] = agent.SignalReceipt{}
			again := snapshot.SignalReceipts()
			preserved, _ := again[2].PendingSignal()
			if !again[2].Matches(late) || string(preserved.Payload()) != `{"follow_up":"retain me"}` {
				t.Fatal("caller mutation corrupted snapshot receipts")
			}
			if snapshot.Usage() != (agent.Usage{CommittedSteps: 3, PreparedEffects: 1, AcceptedSignals: 3}) {
				t.Fatalf("late input accounting=%+v", snapshot.Usage())
			}
		}
		closeEngine(t, restoredEngine)
		closeEngine(t, engine)
	})
}
