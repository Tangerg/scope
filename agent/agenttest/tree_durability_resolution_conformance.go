package agenttest

import (
	"errors"
	"testing"

	agent "github.com/Tangerg/scope/agent"
)

func runCrashBeforeResolvedCommit(t *testing.T, store TreeCommitterConformanceDriver) {
	runCrashResolvedCommit(t, store, crashCommitBefore)
}

func runCrashAfterResolvedCommit(t *testing.T, store TreeCommitterConformanceDriver) {
	runCrashResolvedCommit(t, store, crashCommitAfter)
}

func runCrashResolvedCommit(t *testing.T, store TreeCommitterConformanceDriver, phase crashCommitPhase) {
	t.Helper()
	gate := newTreeCommitterCommitGate(t, store, crashCommitPoint{
		kind: crashCommitEffectResolved, phase: phase,
	})
	step := crashSucceededCall(t)
	step.SettlementStatus = agent.SettlementStatusUnknown
	deployment, dispatcher := newCrashDeployment(t, conformanceModeEffect, agent.ReplayPolicyNever, step)
	engine := newCrashEngine(t, gate, nil)
	original := startConformanceProcess(t, engine, deployment, crashInputValue)
	effectID := waitForConformanceUnknownEffect(t, engine, original)
	resolution := conformanceResolution(t, crashInputValue)
	resolved := make(chan error, 1)
	go func() { resolved <- original.ResolveUnknownEffect(t.Context(), effectID, resolution) }()
	observation := gate.await(t)
	select {
	case err := <-resolved:
		t.Fatalf("resolution returned before its durable acknowledgment: %v", err)
	default:
	}
	head := assertCrashHead(t, store, original.ID(), observation.durableDigest())
	restoredEngine := newCrashEngine(t, store, nil)
	restored := restoreCrashTree(t, restoredEngine, deployment, head)
	if phase == crashCommitBefore {
		if restoredID := waitForConformanceUnknownEffect(t, restoredEngine, restored); restoredID != effectID {
			t.Fatalf("uncommitted resolution changed Unknown identity: %s", restoredID)
		}
		if err := restored.ResolveUnknownEffect(t.Context(), effectID, resolution); err != nil {
			t.Fatal(err)
		}
	}
	assertResolvedContinuation(t, awaitCrashProcess(t, restored))
	requests := dispatcher.Requests()
	if len(requests) != 1 || requests[0].ID() != effectID {
		t.Fatalf("resolution replayed or replaced the external operation: %v", requests)
	}
	gate.abort()
	if err := awaitConformanceValue(t, resolved, "resolution did not return"); !errors.Is(err, errSimulatedHostCrash) {
		t.Fatalf("lost resolution acknowledgment error=%v", err)
	}
	awaitCrashRuntimeError(t, original, errSimulatedHostCrash)
	closeCrashEngine(t, restoredEngine)
	closeCrashEngine(t, engine)
}

func assertResolvedContinuation(t *testing.T, result agent.Result) {
	t.Helper()
	output, present := result.Termination().Output()
	decoded, err := output.Decode[conformanceOutput]()
	if result.Status() != agent.StatusCompleted || !present || err != nil || decoded.Value != crashInputValue {
		t.Fatalf("resolved continuation status=%s output=%+v error=%v", result.Status(), decoded, err)
	}
	wantUsage := agent.Usage{CommittedSteps: 2, PreparedEffects: 1, AcceptedSignals: 1}
	if result.Usage() != wantUsage {
		t.Fatalf("resolved continuation usage=%+v want=%+v", result.Usage(), wantUsage)
	}
}
