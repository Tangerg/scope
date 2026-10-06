package agent

import (
	"context"
	"errors"
	"testing"
)

func TestChildAllocationPreservesPreparedParentWork(t *testing.T) {
	for _, committer := range []struct {
		name      string
		recording bool
	}{
		{name: "in memory"},
		{name: "recording", recording: true},
	} {
		for _, test := range []struct {
			name              string
			budget            Budget
			maxPendingSignals uint64
			wantChildren      int
		}{
			{name: "steps reserved", budget: Budget{Steps: NewQuota(20)}},
			{name: "effects charged", budget: Budget{Effects: NewQuota(20)}},
			{name: "signals reserved", budget: Budget{Signals: NewQuota(40)}, maxPendingSignals: 40},
			{
				name: "exact fit", budget: Budget{Steps: NewQuota(22), Effects: NewQuota(21), Signals: NewQuota(41)}, maxPendingSignals: 41,
				wantChildren: 1,
			},
		} {
			t.Run(committer.name+"/"+test.name, func(t *testing.T) {
				config := EngineConfig{
					TreeCommitter: NewMemoryTreeCommitter(), Budget: test.budget,
					TreeLimits: TreeLimits{MaxPendingSignals: test.maxPendingSignals},
				}
				if committer.recording {
					config.TreeCommitter = &recordingTreeCommitter{}
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
	if err := wire.validateCapacity(resourceAmounts{}); err != nil {
		t.Fatal(err)
	}
	children := resourceAmounts{Steps: wire.Budget.Steps.maximum - wire.CommittedSteps}
	if err := wire.validateCapacity(children); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("unfunded prepared Step error=%v", err)
	}
}

func TestRejectedChildSettlementReleasesUnpublishedStart(t *testing.T) {
	runtime, parent := newChildCompletionTestProcess(t)
	engine := runtime.engine
	if err := engine.reserveProcessStart(parent.handle.relation); err != nil {
		t.Fatal(err)
	}
	engine.publishProcessStart(parent.handle)
	effectID := parent.handle.processID.effectID(1, 0)
	key, _ := ParseChildKey("worker")
	input, _ := EncodePayload(childTestInput{Mode: "leaf"})
	spec := childTestSpec(key, parent.deployment().DeploymentRef(), input)
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
	if parent.status() != StatusFailed {
		t.Fatalf("rejected settlement parent status=%s", parent.status())
	}
	if _, exists := engine.Process(prepared.plan.childID()); exists {
		t.Fatal("rejected child settlement published the child")
	}
	if runtime.childDebits(parent) != (resourceAmounts{}) || runtime.members.len() != 1 {
		t.Fatal("rejected child settlement retained its budget or prospective Process")
	}
	assertTreeMembership(t, runtime)
	assertNoPendingProcessStarts(t, engine)
	if err := engine.reserveProcessStart(prepared.plan.relation); err != nil {
		t.Fatalf("released child identity and key could not be reserved again: %v", err)
	}
	engine.discardProcessStart(prepared.plan.relation)
	go runtime.run(t.Context())
	if err := (&Process{handle: parent.handle}).Join(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestTreeAdmissionCountsInFlightSiblingStartsAndInstalledChildrenOnce(t *testing.T) {
	runtime := newWaitingSnapshotTree(t, 3)
	root := runtime.members.get(runtime.rootID)
	children := runtime.members.childrenOf(runtime.rootID)
	first, second := runtime.members.get(children[0]), runtime.members.get(children[1])
	limits := TreeLimits{MaxPendingSignals: runtime.treeLimits.MaxPendingSignals, MaxDepth: 2, MaxChildren: NewQuota(2), MaxActiveChildren: 1, MaxTreeProcesses: NewQuota(4)}
	runtime.treeLimits = limits
	if !runtime.canStartChild(first) || !runtime.canStartChild(second) {
		t.Fatal("free tree slot was rejected")
	}
	childID := first.handle.processID.effectID(1, 0).childProcessID()
	relation := childProcessRelation(childID, first.handle.relation, controlValue(ParseChildKey("worker")))
	runtime.jobs.start(first.handle.processID, &processJob{kind: processJobChildStart, childStart: &childStartPlan{relation: relation}})
	if runtime.canStartChild(second) {
		t.Fatal("sibling start ignored the last in-flight tree slot")
	}
	runtime.treeLimits.MaxTreeProcesses = NewQuota(5)
	if runtime.canStartChild(first) || !runtime.canStartChild(second) {
		t.Fatal("in-flight start did not retain its parent's active-child slot")
	}
	handle := newProcessHandle(relation, first.deployment(), first.handle.budget, first.handle.capabilities, root.handle.startedAt)
	child := newProcessState(handle, first.execution, first.committedExecutionState)
	runtime.addProcess(child)
	if !runtime.canStartChild(second) {
		t.Fatal("installed child and its pending publication were counted twice")
	}
	if _, finished := runtime.jobs.finish(treeJobCompletion{processID: first.handle.processID, result: childStartJobResult{}}); !finished {
		t.Fatal("in-flight child start was not retired")
	}
	runtime.removeProcess(childID)
	if !runtime.canStartChild(first) || !runtime.canStartChild(second) {
		t.Fatal("discarded child retained a resource reservation")
	}
}

func TestChildStartJobOwnsItsProvisionalGrant(t *testing.T) {
	runtime, process := newChildCompletionTestProcess(t)
	budget := Budget{Steps: NewQuota(1), Effects: NewQuota(2), Signals: NewQuota(3)}
	childID := process.handle.processID.effectID(1, 0).childProcessID()
	relation := childProcessRelation(childID, process.handle.relation, controlValue(ParseChildKey("provisional")))
	job := &processJob{kind: processJobChildStart, childStart: &childStartPlan{relation: relation, spec: ChildSpec{Budget: budget}}}
	runtime.jobs.start(process.handle.processID, job)
	want, _ := process.handle.budget.allocation(budget)
	if got := runtime.childDebits(process); got != want {
		t.Fatalf("in-flight start debits = %+v, want %+v", got, want)
	}
	runtime.jobs.finish(treeJobCompletion{processID: process.handle.processID, result: childStartJobResult{}})
	if got := runtime.childDebits(process); got != (resourceAmounts{}) {
		t.Fatalf("finished start retained debits %+v", got)
	}
}

func TestChildPublicationRequiresTheReservedRequestDigest(t *testing.T) {
	runtime, parent := newChildCompletionTestProcess(t)
	engine := runtime.engine
	if err := engine.reserveProcessStart(parent.handle.relation); err != nil {
		t.Fatal(err)
	}
	engine.publishProcessStart(parent.handle)
	key := controlValue(ParseChildKey("worker"))
	relation := childProcessRelation(newProcessID(), parent.handle.relation, key)
	if err := engine.reserveProcessStart(relation); err != nil {
		t.Fatal(err)
	}
	for _, reserved := range []bool{false, true} {
		handleRelation := relation
		if !reserved {
			handleRelation = childProcessRelation(newProcessID(), parent.handle.relation, key)
		}
		handle := newProcessHandle(handleRelation, parent.deployment(),
			parent.handle.budget, parent.handle.capabilities, parent.handle.startedAt)
		handle.tree = &runtime.binding
		published := func() (published bool) {
			defer func() { published = recover() == nil }()
			engine.publishProcessStart(handle)
			return
		}()
		if published != reserved {
			t.Fatalf("reserved=%v published=%v", reserved, published)
		}
	}
	child, found := engine.Process(relation.ProcessID())
	if !found {
		t.Fatal("reserved child was not published")
	}
	for _, handle := range []*processHandle{parent.handle, child.handle} {
		handle.publishResult(Result{})
		handle.finishBookkeeping()
	}
}
