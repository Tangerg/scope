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
