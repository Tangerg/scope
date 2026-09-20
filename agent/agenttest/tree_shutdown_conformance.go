package agenttest

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent"
)

func runDetachedStorageShutdownConformance(t *testing.T, factory func() TreeCommitterConformanceDriver) {
	for _, phase := range []crashCommitPhase{crashCommitBefore, crashCommitAfter} {
		name := "before commit"
		if phase == crashCommitAfter {
			name = "after commit"
		}
		t.Run(name, func(t *testing.T) {
			store := factory()
			gate := newTreeCommitterCommitGate(t, store, crashCommitPoint{kind: crashCommitCheckpointProgress, phase: phase})
			deployment, _ := newCrashDeployment(t, conformanceModeProgress, agent.ReplayPolicyNever)
			engine := newCrashEngine(t, gate, nil)
			caller, cancel := context.WithTimeout(t.Context(), conformanceStatusTimeout)
			defer cancel()
			input, err := deployment.Descriptor().EncodeInput(conformanceInput{Value: crashInputValue})
			if err != nil {
				t.Fatal(err)
			}
			process, err := engine.Start(caller, deployment, input)
			if err != nil {
				t.Fatal(err)
			}
			observation := gate.await(t)
			cancel()
			if observation.ctx.Done() != nil || observation.ctx.Err() != nil {
				t.Fatal("caller cancellation reached the storage transaction")
			}
			if _, present := observation.ctx.Deadline(); present {
				t.Fatal("storage inherited a caller deadline")
			}
			if err := process.Join(caller); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled Join: %v", err)
			}
			if err := engine.Close(t.Context()); !errors.Is(err, agent.ErrEngineHasActiveProcesses) {
				t.Fatalf("Close abandoned blocked storage: %v", err)
			}
			gate.abort()
			awaitCrashRuntimeError(t, process, errSimulatedHostCrash)
			ctx, release := context.WithTimeout(t.Context(), conformanceStatusTimeout)
			defer release()
			if err := process.Join(ctx); !errors.Is(err, errSimulatedHostCrash) {
				t.Fatalf("Join did not drain failed storage: %v", err)
			}
			want := observation.previousDigest
			if phase == crashCommitAfter {
				want = observation.prospective.Digest()
			}
			assertCrashHead(t, store, observation.rootID, want)
			closeCrashEngine(t, engine)
		})
	}
}
