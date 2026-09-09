package agenttest

import (
	"context"
	"errors"
	"testing"

	agent "github.com/Tangerg/scope/agent"
)

func runCrashBeforeResolvedCommit(t *testing.T, store TreeDurabilityConformanceDriver) {
	runCrashResolvedCommit(t, store, crashCommitBefore)
}

func runCrashAfterResolvedCommit(t *testing.T, store TreeDurabilityConformanceDriver) {
	runCrashResolvedCommit(t, store, crashCommitAfter)
}

func runCrashResolvedCommit(t *testing.T, store TreeDurabilityConformanceDriver, phase crashCommitPhase) {
	t.Helper()
	durability := store.TreeDurability()
	gate := newTreeDurabilityCommitGate(t, durability, crashCommitPoint{
		kind: crashCommitEffectResolved, phase: phase,
	})
	step := crashSucceededDispatchStep(t)
	step.SettlementStatus = agent.SettlementStatusUnknown
	deployment, dispatcher := newCrashDeployment(t, conformanceModeEffect, agent.ReplayPolicyNever, step)
	engine := newCrashEngine(t, gate, nil)
	original := startCrashProcess(t, engine, deployment)
	effectID := waitForConformanceUnknownEffect(t, engine, original)
	resolution := crashResolution(t, effectID)
	resolved := make(chan error, 1)
	go func() { resolved <- original.ResolveUnknownEffect(t.Context(), resolution) }()
	observation := gate.await(t)
	select {
	case err := <-resolved:
		t.Fatalf("resolution returned before its durable acknowledgment: %v", err)
	default:
	}
	wantHead := observation.previousDigest
	if phase == crashCommitAfter {
		wantHead = observation.prospective.Digest()
	}
	head := assertCrashHead(t, store, original.ID(), wantHead)
	restoredEngine := newCrashEngine(t, durability, nil)
	restored := restoreCrashTree(t, restoredEngine, deployment, head)
	if phase == crashCommitBefore {
		if restoredID := waitForConformanceUnknownEffect(t, restoredEngine, restored); restoredID != effectID {
			t.Fatalf("uncommitted resolution changed Unknown identity: %s", restoredID)
		}
		if err := restored.ResolveUnknownEffect(t.Context(), resolution); err != nil {
			t.Fatal(err)
		}
	}
	result := awaitCrashProcess(t, restored)
	output, present := result.Output()
	decoded, err := output.Decode[conformanceOutput]()
	if result.Status() != agent.StatusCompleted || !present || err != nil || decoded.Value != crashInputValue {
		t.Fatalf("resolved continuation status=%s output=%+v error=%v", result.Status(), decoded, err)
	}
	wantUsage := agent.Usage{CommittedSteps: 2, PreparedEffects: 1, AcceptedSignals: 1}
	if result.Usage() != wantUsage {
		t.Fatalf("resolved continuation usage=%+v want=%+v", result.Usage(), wantUsage)
	}
	requests := dispatcher.Requests()
	if len(requests) != 1 || requests[0].ID() != effectID {
		t.Fatalf("resolution replayed or replaced the external operation: %v", requests)
	}
	gate.abort()
	ctx, cancel := context.WithTimeout(t.Context(), conformanceStatusTimeout)
	defer cancel()
	select {
	case err := <-resolved:
		if !errors.Is(err, errSimulatedHostCrash) {
			t.Fatalf("lost resolution acknowledgment error=%v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	awaitCrashRuntimeError(t, original, errSimulatedHostCrash)
	closeCrashEngine(t, restoredEngine)
	closeCrashEngine(t, engine)
}
