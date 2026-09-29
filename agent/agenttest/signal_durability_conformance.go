package agenttest

import (
	"errors"
	"testing"

	agent "github.com/Tangerg/scope/agent"
)

type signalDeliveryResult struct {
	accepted bool
	err      error
}

type signalAdmissionScenario struct {
	name  string
	phase crashCommitPhase
	crash bool
}

// durable reports whether the admission reached storage: either the commit
// was acknowledged or the host crashed only after the head advanced.
func (s signalAdmissionScenario) durable() bool {
	return !s.crash || s.phase == crashCommitAfter
}

func (s signalAdmissionScenario) release(gate *treeCommitterCommitGate) {
	if s.crash {
		gate.abort()
		return
	}
	gate.continueCommit()
}

func runSignalAdmissionConformance(t *testing.T, factory func() TreeCommitterConformanceDriver) {
	t.Helper()
	for _, scenario := range []signalAdmissionScenario{
		{name: "acknowledgment follows publication", phase: crashCommitBefore},
		{name: "failure before commit", phase: crashCommitBefore, crash: true},
		{name: "response lost after commit", phase: crashCommitAfter, crash: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			runSignalAdmission(t, factory(), scenario)
		})
	}
}

func runSignalAdmission(t *testing.T, driver TreeCommitterConformanceDriver, scenario signalAdmissionScenario) {
	gate := newTreeCommitterCommitGate(t, driver, crashCommitPoint{
		kind: crashCommitCheckpointInput, phase: scenario.phase,
	})
	recorder := &ObservationRecorder{}
	engine, err := agent.NewEngine(agent.EngineConfig{
		TreeCommitter: gate, EventListeners: []agent.EventListener{recorder},
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment := conformanceDeployment(t, conformanceModePause)
	process := startConformanceProcess(t, engine, deployment, "signal admission")
	t.Cleanup(func() {
		gate.abort()
		closeConformanceProcess(t, engine, process)
	})
	before := waitForConformanceHeadStatus(t, driver, process.ID(), agent.StatusPaused)
	// A reader can see the stored pause before its acknowledgment has
	// returned to the Engine. Establish both sides before taking usage.
	waitForConformanceStatus(t, engine, process, agent.StatusPaused)
	usage := inspectConformanceProcess(t, engine, process).Usage()
	request := durableSignalRequest(t, `{"value":"accepted"}`)
	delivered := make(chan signalDeliveryResult, 1)
	go func() {
		accepted, deliveryErr := process.DeliverSignals(t.Context(), request)
		delivered <- signalDeliveryResult{accepted: accepted, err: deliveryErr}
	}()
	observation := gate.await(t)
	select {
	case response := <-delivered:
		t.Fatalf("delivery acknowledged before committer returned: %+v", response)
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

	scenario.release(gate)
	response := awaitConformanceValue(t, delivered, "signal delivery did not return")
	if scenario.crash {
		if response.accepted || !errors.Is(response.err, errSimulatedHostCrash) {
			t.Fatalf("uncertain delivery = %+v", response)
		}
		assertCrashEventAbsent(t, recorder, agent.EventSignalAccepted)
		awaitCrashRuntimeError(t, process, errSimulatedHostCrash)
	} else {
		if !response.accepted || response.err != nil {
			t.Fatalf("acknowledged delivery = %+v", response)
		}
		wantHead = observation.prospective.Digest()
	}
	head := assertCrashHead(t, driver, process.ID(), wantHead)
	assertDurableSignal(t, head, request, scenario.durable())
	assertSignalRetryAfterRecovery(t, driver, deployment, head, request, usage, scenario.durable())
}

func assertSignalRetryAfterRecovery(
	t *testing.T,
	driver TreeCommitterConformanceDriver,
	deployment agent.Deployment,
	head agent.TreeSnapshot,
	request agent.SignalRequest,
	usage agent.Usage,
	durable bool,
) {
	t.Helper()
	engine := newConformanceEngine(t, driver)
	restored := restoreCrashTree(t, engine, deployment, head)
	t.Cleanup(func() { closeConformanceProcess(t, engine, restored) })
	accepted, err := restored.DeliverSignals(t.Context(), request)
	if err != nil || accepted == durable {
		t.Fatalf("retry after recovery accepted=%t previously committed=%t error=%v", accepted, durable, err)
	}
	if recovered := inspectConformanceProcess(t, engine, restored).Usage(); recovered.AcceptedSignals != usage.AcceptedSignals+1 {
		t.Fatalf("recovered input charged more than once: %+v", recovered)
	}
	conflict := durableSignalRequest(t, `{"value":"conflict"}`)
	if accepted, err := restored.DeliverSignals(t.Context(), conflict); accepted || !errors.Is(err, agent.ErrSignalConflict) {
		t.Fatalf("conflicting retry accepted=%t error=%v", accepted, err)
	}
}

func durableSignalRequest(t *testing.T, payload string) agent.SignalRequest {
	t.Helper()
	id, err := agent.ParseSignalID("signal:durable-input")
	if err != nil {
		t.Fatal(err)
	}
	request, err := agent.NewSignalRequest(id, agent.WaitID{}, []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func assertDurableSignal(t *testing.T, snapshot agent.TreeSnapshot, request agent.SignalRequest, present bool) {
	t.Helper()
	root := conformanceSnapshotByID(snapshot.ProcessSnapshots(), snapshot.RootID())
	receipts := root.SignalReceipts()
	wantCount := 0
	if present {
		wantCount = 1
	}
	if len(receipts) != wantCount {
		t.Fatalf("durable input count=%d want=%d", len(receipts), wantCount)
	}
	if !present {
		return
	}
	signal, pending := receipts[0].PendingSignal()
	if !pending || !receipts[0].Matches(request) || string(signal.Payload()) != string(request.Payload()) {
		t.Fatalf("durable input=%s %s", signal.ID(), signal.Payload())
	}
}
