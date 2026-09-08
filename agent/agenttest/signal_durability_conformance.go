package agenttest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	agent "github.com/Tangerg/scope/agent"
)

type signalDeliveryResult struct {
	accepted bool
	err      error
}

func runSignalAdmissionConformance(t *testing.T, factory func() TreeDurabilityConformanceDriver) {
	t.Helper()
	for _, scenario := range []struct {
		name  string
		phase crashCommitPhase
		crash bool
	}{
		{name: "acknowledgment follows publication", phase: crashCommitBefore},
		{name: "failure before commit", phase: crashCommitBefore, crash: true},
		{name: "response lost after commit", phase: crashCommitAfter, crash: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			driver := factory()
			gate := newTreeDurabilityCommitGate(t, driver.TreeDurability(), crashCommitPoint{
				kind: crashCommitCheckpointInput, phase: scenario.phase,
			})
			recorder := &ObservationRecorder{}
			engine, err := agent.NewEngine(agent.EngineConfig{
				TreeDurability: gate, EventListeners: []agent.EventListener{recorder},
			})
			if err != nil {
				t.Fatal(err)
			}
			deployment := conformanceDeployment(t, conformanceModePause)
			input, err := deployment.Descriptor().EncodeInput(conformanceInput{Value: "signal admission"})
			if err != nil {
				t.Fatal(err)
			}
			process, err := engine.Start(context.Background(), deployment, input)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				gate.abort()
				closeSignalConformanceProcess(t, engine, process)
			})
			before := waitForConformanceHeadStatus(t, driver, process.ID(), agent.StatusPaused)
			// A reader can see the stored pause before its acknowledgment has
			// returned to the Engine. Establish both sides before taking usage.
			waitForConformanceStatus(t, engine, process, agent.StatusPaused)
			usage := inspectConformanceProcess(t, engine, process).Usage()
			id, err := agent.ParseSignalID("signal:durable-input")
			if err != nil {
				t.Fatal(err)
			}
			request, err := agent.NewSignalRequest(id, agent.WaitID{}, []byte(`{"value":"accepted"}`))
			if err != nil {
				t.Fatal(err)
			}
			delivered := make(chan signalDeliveryResult, 1)
			go func() {
				accepted, deliveryErr := process.DeliverSignals(context.Background(), request)
				delivered <- signalDeliveryResult{accepted: accepted, err: deliveryErr}
			}()
			observation := gate.await(t)
			select {
			case response := <-delivered:
				t.Fatalf("delivery acknowledged before durability returned: %+v", response)
			default:
			}
			if inspectConformanceProcess(t, engine, process).Usage() != usage {
				t.Fatal("unacknowledged input changed published resource usage")
			}
			assertCrashEventAbsent(t, recorder, agent.EventSignalAccepted)
			wantHead := before.Digest()
			if scenario.phase == crashCommitAfter {
				wantHead = observation.prospective.Digest()
			}
			assertCrashHead(t, driver, process.ID(), wantHead)
			if scenario.crash {
				gate.abort()
			} else {
				gate.continueCommit()
			}
			ctx, cancel := context.WithTimeout(t.Context(), conformanceStatusTimeout)
			defer cancel()
			select {
			case response := <-delivered:
				if scenario.crash {
					if response.accepted || !errors.Is(response.err, errSimulatedHostCrash) {
						t.Fatalf("uncertain delivery = %+v", response)
					}
					assertCrashEventAbsent(t, recorder, agent.EventSignalAccepted)
					awaitCrashRuntimeError(t, process, errSimulatedHostCrash)
				} else if !response.accepted || response.err != nil {
					t.Fatalf("acknowledged delivery = %+v", response)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if !scenario.crash {
				wantHead = observation.prospective.Digest()
			}
			head := assertCrashHead(t, driver, process.ID(), wantHead)
			hasInput := !scenario.crash || scenario.phase == crashCommitAfter
			assertDurableSignal(t, head, id, hasInput)
			restoredEngine, err := agent.NewEngine(agent.EngineConfig{TreeDurability: driver.TreeDurability()})
			if err != nil {
				t.Fatal(err)
			}
			restored, err := restoredEngine.RestoreTree(context.Background(), deployment, head)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				closeSignalConformanceProcess(t, restoredEngine, restored)
			})
			accepted, err := restored.DeliverSignals(t.Context(), request)
			if err != nil || accepted == hasInput {
				t.Fatalf("retry after recovery accepted=%t previously committed=%t error=%v", accepted, hasInput, err)
			}
			if inspectConformanceProcess(t, restoredEngine, restored).Usage().AcceptedSignals != usage.AcceptedSignals+1 {
				t.Fatalf("recovered input charged more than once: %+v", inspectConformanceProcess(t, restoredEngine, restored).Usage())
			}
			conflict, err := agent.NewSignalRequest(id, agent.WaitID{}, []byte(`{"value":"conflict"}`))
			if err != nil {
				t.Fatal(err)
			}
			if accepted, err := restored.DeliverSignals(t.Context(), conflict); accepted || !errors.Is(err, agent.ErrSignalConflict) {
				t.Fatalf("conflicting retry accepted=%t error=%v", accepted, err)
			}
		})
	}
}

func closeSignalConformanceProcess(t *testing.T, engine *agent.Engine, process *agent.Process) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), conformanceStatusTimeout)
	defer cancel()
	if err := process.Kill(ctx, "signal conformance cleanup"); err != nil && !errors.Is(err, agent.ErrProcessFinished) &&
		!errors.Is(err, errSimulatedHostCrash) && !errors.Is(err, agent.ErrTreeIncarnationConflict) {
		t.Error(err)
	}
	if _, err := process.Await(ctx); err != nil && !errors.Is(err, errSimulatedHostCrash) && !errors.Is(err, agent.ErrTreeIncarnationConflict) {
		t.Error(err)
	}
	if err := engine.Close(); err != nil {
		t.Error(err)
	}
}

func assertDurableSignal(t *testing.T, snapshot agent.TreeSnapshot, id agent.SignalID, present bool) {
	t.Helper()
	root := conformanceSnapshotByID(snapshot.ProcessSnapshots(), snapshot.RootID())
	var wire struct {
		Mailbox struct {
			Signals []struct {
				ID      agent.SignalID  `json:"id"`
				Payload json.RawMessage `json:"payload"`
			} `json:"signals"`
		} `json:"mailbox"`
	}
	if err := json.Unmarshal(root.JSON(), &wire); err != nil {
		t.Fatal(err)
	}
	wantCount := 0
	if present {
		wantCount = 1
	}
	if len(wire.Mailbox.Signals) != wantCount {
		t.Fatalf("durable input count=%d want=%d", len(wire.Mailbox.Signals), wantCount)
	}
	if present {
		signal := wire.Mailbox.Signals[0]
		if signal.ID != id || string(signal.Payload) != `{"value":"accepted"}` {
			t.Fatalf("durable input=%s %s", signal.ID, signal.Payload)
		}
	}
}
