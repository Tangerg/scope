package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestChildAllocationPreservesPreparedParentWork(t *testing.T) {
	for _, durability := range []struct {
		name  string
		store bool
	}{
		{name: "in memory"},
		{name: "durable", store: true},
	} {
		for _, test := range []struct {
			name         string
			limits       Limits
			wantChildren int
		}{
			{name: "steps reserved", limits: Limits{MaxSteps: 20}},
			{name: "effects charged", limits: Limits{MaxEffects: 20}},
			{name: "signals reserved", limits: Limits{MaxSignals: 40, MaxPendingSignals: 40}},
			{
				name: "exact fit", limits: Limits{MaxSteps: 22, MaxEffects: 21, MaxSignals: 41, MaxPendingSignals: 41},
				wantChildren: 1,
			},
		} {
			t.Run(durability.name+"/"+test.name, func(t *testing.T) {
				config := EngineConfig{Limits: test.limits}
				if durability.store {
					config.TreeDurability = &recordingTreeDurability{}
				}
				engine, err := NewEngine(config)
				if err != nil {
					t.Fatal(err)
				}
				input, err := EncodeInput(childTestInput{Mode: "parent"})
				if err != nil {
					t.Fatal(err)
				}
				root, err := engine.Start(t.Context(), newChildTestDeployment(t), input)
				if err != nil {
					t.Fatal(err)
				}
				result, err := root.Await(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				ids := directChildIDs(t, engine, root.ID())
				awaitChildren(t, engine, ids)
				snapshot := inspectProcessSnapshot(t, root)
				if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
					t.Fatal(closeErr)
				}
				if result.Status() != StatusCompleted || len(ids) != test.wantChildren || !snapshot.Valid() {
					t.Fatalf("parent=%s children=%v snapshot valid=%t", result.Status(), ids, snapshot.Valid())
				}
				output := childTestResult(t, result)
				if test.wantChildren == 0 {
					if output.Failures != 1 || len(output.FailureCodes) != 1 || output.FailureCodes[0] != "engine.child.budget_exhausted" {
						t.Fatalf("rejected child output=%+v", output)
					}
				} else if output.Failures != 0 {
					t.Fatalf("accepted child output=%+v", output)
				}
			})
		}
	}
}

func TestSnapshotRejectsChildBudgetThatConsumesPreparedStep(t *testing.T) {
	snapshot := preparedEngineTestSnapshot(t)
	wire, err := snapshot.wire()
	if err != nil {
		t.Fatal(err)
	}
	wire.ReservedBudget.Steps = wire.Budget.Steps - wire.CommittedSteps
	data, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if _, parseErr := ParseProcessSnapshot(data); !errors.Is(parseErr, ErrInvalidSnapshot) {
		t.Fatalf("unfunded prepared Step error=%v", parseErr)
	}
}

func TestRejectedChildSettlementReleasesUnpublishedStart(t *testing.T) {
	runtime, parent := newChildCompletionTestProcess(t)
	engine := runtime.engine
	if err := engine.reserveProcessStart(parent.handle.relation, parent.deployment.DeploymentRef(), parent.treeLimits, Digest{}); err != nil {
		t.Fatal(err)
	}
	engine.publishReservedProcess(parent.handle)
	effectID := deriveEffectID(parent.handle.processID, 1, 0)
	key, _ := ParseChildKey("worker")
	input, _ := EncodeInput(childTestInput{Mode: "leaf"})
	spec := childTestSpec(key, parent.deployment.DeploymentRef(), input)
	prepared := runtime.prepareChildStart(parent, effectID, spec)
	if prepared.plan == nil {
		t.Fatalf("prepare child failed: %+v", prepared.result)
	}
	result := prepared.plan.execute(t.Context())
	if !result.started() {
		t.Fatalf("initialize child failed: %+v", result.result)
	}
	// Reject settlement after initialization to exercise cleanup after the
	// child's budget and prospective Process have both been installed.
	runtime.applyChildStartCompletion(parent, &processJob{
		childStart: prepared.plan, effectID: effectID, startedAt: result.startedAt,
	}, result)
	if parent.status != StatusFailed {
		t.Fatalf("rejected settlement parent status=%s", parent.status)
	}
	if _, exists := engine.Process(prepared.plan.childID); exists {
		t.Fatal("rejected child settlement published the child")
	}
	if parent.effectiveReservedBudget() != (Budget{}) || len(runtime.processes) != 1 {
		t.Fatal("rejected child settlement retained its budget or prospective Process")
	}
	assertNoPendingProcessStarts(t, engine)
	if err := engine.reserveProcessStart(prepared.plan.relation, spec.DeploymentRef, parent.treeLimits, prepared.plan.requestDigest); err != nil {
		t.Fatalf("released child identity and key could not be reserved again: %v", err)
	}
	engine.discardProcessStartReservation(prepared.plan.childID)
}
