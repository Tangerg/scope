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
			{name: "steps reserved", limits: Limits{Budget: Budget{Steps: NewQuota(20)}}},
			{name: "effects charged", limits: Limits{Budget: Budget{Effects: NewQuota(20)}}},
			{name: "signals reserved", limits: Limits{MaxPendingSignals: 40, Budget: Budget{Signals: NewQuota(40)}}},
			{
				name: "exact fit", limits: Limits{MaxPendingSignals: 41, Budget: Budget{Steps: NewQuota(22), Effects: NewQuota(21), Signals: NewQuota(41)}},
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
				input, err := EncodePayload(childTestInput{Mode: "parent"})
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
	wire.AllocatedResources.Steps = wire.Limits.Budget.Steps.maximum - wire.CommittedSteps
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
	engine.publishProcessStart(parent.handle)
	effectID := parent.handle.processID.effectID(1, 0)
	key, _ := ParseChildKey("worker")
	input, _ := EncodePayload(childTestInput{Mode: "leaf"})
	spec := childTestSpec(key, parent.deployment.DeploymentRef(), input)
	prepared := runtime.prepareChildStart(parent, effectID, spec)
	if prepared.plan == nil {
		t.Fatalf("prepare child failed: %+v", prepared.result)
	}
	result := prepared.plan.execute(t.Context())
	if !result.started() {
		t.Fatalf("initialize child failed: %+v", result.result)
	}
	// Initialization succeeded, but no pending Effect can accept its settlement.
	runtime.applyChildStartCompletion(parent, &processJob{
		childStart: prepared.plan, effectID: effectID, effectAttempt: effectAttempt{id: newEffectAttemptID(), startedAt: result.startedAt},
	}, result)
	if parent.status != StatusFailed {
		t.Fatalf("rejected settlement parent status=%s", parent.status)
	}
	if _, exists := engine.Process(prepared.plan.childID); exists {
		t.Fatal("rejected child settlement published the child")
	}
	if parent.effectiveAllocations() != (resourceAmounts{}) || len(runtime.processes) != 1 {
		t.Fatal("rejected child settlement retained its budget or prospective Process")
	}
	assertTreeMembership(t, runtime)
	assertNoPendingProcessStarts(t, engine)
	if err := engine.reserveProcessStart(prepared.plan.relation, spec.DeploymentRef, parent.treeLimits, prepared.plan.requestDigest); err != nil {
		t.Fatalf("released child identity and key could not be reserved again: %v", err)
	}
	engine.discardProcessStart(prepared.plan.childID)
}

func TestTreeAdmissionCountsInFlightSiblingStartsAndInstalledChildrenOnce(t *testing.T) {
	runtime := newWaitingSnapshotTree(t, 3)
	root := runtime.processes[runtime.rootID]
	children := runtime.childrenByParent[runtime.rootID]
	first, second := runtime.processes[children[0]], runtime.processes[children[1]]
	limits := TreeLimits{MaxDepth: 2, MaxChildren: NewQuota(2), MaxActiveChildren: 1, MaxTreeProcesses: NewQuota(4)}
	for _, process := range runtime.processes {
		process.treeLimits = limits
	}
	if !runtime.canStartChild(first) || !runtime.canStartChild(second) {
		t.Fatal("free tree slot was rejected")
	}
	childID := first.handle.processID.effectID(1, 0).childProcessID()
	relation := childProcessRelation(childID, first.handle.relation, controlValue(ParseChildKey("worker")))
	runtime.jobs[first.handle.processID] = &processJob{kind: processJobChildStart, childStart: &childStartPlan{childID: childID, relation: relation}}
	if runtime.canStartChild(second) {
		t.Fatal("sibling start ignored the last in-flight tree slot")
	}
	for _, process := range runtime.processes {
		process.treeLimits.MaxTreeProcesses = NewQuota(5)
	}
	if runtime.canStartChild(first) || !runtime.canStartChild(second) {
		t.Fatal("in-flight start did not retain its parent's active-child slot")
	}
	handle := newProcessHandle(relation, first.deployment.DeploymentRef(), first.limits.Budget, first.capabilities, first.treeLimits, root.startedAt)
	child := newProcessState(handle, first.deployment, first.execution, first.committedExecutionState, root.startedAt, runtime.engine.limits)
	runtime.addProcess(child)
	if !runtime.canStartChild(second) {
		t.Fatal("installed child and its pending publication were counted twice")
	}
	delete(runtime.jobs, first.handle.processID)
	runtime.removeProcess(childID)
	if !runtime.canStartChild(first) || !runtime.canStartChild(second) {
		t.Fatal("discarded child retained a resource reservation")
	}
}

func TestProvisionalBudgetReleaseRequiresExactReservation(t *testing.T) {
	_, process := newChildCompletionTestProcess(t)
	budget := Budget{Steps: NewQuota(1), Effects: NewQuota(2), Signals: NewQuota(3)}
	process.provisionalChildBudget = new(budget)
	func() {
		defer func() {
			if recover() == nil {
				t.Error("mismatched release was silently ignored")
			}
		}()
		process.releaseProvisionalChildBudget(Budget{Steps: NewQuota(1), Effects: NewQuota(2), Signals: NewQuota(2)})
	}()
	if *process.provisionalChildBudget != budget {
		t.Fatal("rejected release changed the reservation")
	}
	process.releaseProvisionalChildBudget(budget)
	if process.provisionalChildBudget != nil {
		t.Fatal("exact release retained the reservation")
	}
}
