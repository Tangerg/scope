package agent

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestFrameworkParsersRejectCallerOwnedSignals(t *testing.T) {
	deployment := newChildTestDeployment(t)
	effectID := controlValue(ParseProcessID("process:framework")).effectID(1, 0)
	childID := effectID.childProcessID()
	key := controlValue(ParseChildKey("child"))
	waitID := effectID.waitID()
	spec := ChildWaitSpec{Key: controlValue(ParseWaitKey("children")), Children: []ProcessID{childID}, Boundary: ChildWaitBoundaryDrained, Condition: AllChildren()}
	start := ChildStartResult{key: key, processID: childID, deploymentRef: deployment.DeploymentRef()}
	control := ChildControlResult{childID: childID, operation: frameworkEffectCancelChild}
	failure := controlValue(NewFailure(FailureKindExecution, "test.failed", "test failure"))
	now := time.Now().UTC()
	result := Result{processID: childID, startedAt: now, finishedAt: now, termination: failure.termination()}
	completed := controlValue(encodeChildWaitSatisfied(waitID, spec.Key, spec.Boundary, []ChildOutcome{{key: key, result: result, subtreeUnresolvedEffects: []UnresolvedEffect{}}}))
	for _, test := range []struct {
		name   string
		signal Signal
		parse  func(Signal) error
	}{
		{"start", controlValue(newSignal(effectID.settlementSignalID(), WaitID{}, controlValue(encodeChildStartResult(start)))), func(signal Signal) error { _, err := ParseChildStartResult(signal); return err }},
		{"control", controlValue(newSignal(effectID.settlementSignalID(), WaitID{}, controlValue(json.Marshal(control)))), func(signal Signal) error { _, err := ParseChildControlResult(signal); return err }},
		{"opening", controlValue(newSignal(effectID.settlementSignalID(), waitID, controlValue(encodeChildWaitOpened(spec)))), func(signal Signal) error { _, err := ParseChildWaitOpened(signal); return err }},
		{"completion", completed, func(signal Signal) error { _, err := ParseChildWaitSatisfied(signal); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.parse(test.signal); err != nil {
				t.Fatalf("invalid Engine fixture: %v", err)
			}
			addressed, _ := test.signal.WaitID()
			request := controlValue(NewSignalRequest(controlValue(ParseSignalID("signal:caller")), addressed, test.signal.Payload()))
			if err := test.parse(controlValue(request.signal())); err == nil {
				t.Fatal("caller payload was accepted as Framework evidence")
			}
		})
	}
}

func TestWaitSettlementMustMatchDeclaredRequest(t *testing.T) {
	wire := controlValue(preparedEngineTestSnapshot(t).wire())
	key := controlValue(ParseWaitKey("wait"))
	childID := controlValue(ParseProcessID("process:child"))
	for _, effect := range []Effect{
		controlValue(RequestWait(key, []byte(`{"prompt":"original"}`))),
		controlValue(WaitForChildren(ChildWaitSpec{Key: key, Children: []ProcessID{childID}, Boundary: ChildWaitBoundaryDrained, Condition: AllChildren()})),
	} {
		for _, mutation := range []string{"status", "payload"} {
			t.Run(string(effect.Payload())+"/"+mutation, func(t *testing.T) {
				candidate := wire.clone()
				candidate.Prepared.Transition = controlValue(Continue(0, effect))
				record := preparedEffect{ID: candidate.ProcessID.effectID(1, 0), Effect: effect, Phase: effectPhasePending}
				if err := record.settleFramework(); err != nil {
					t.Fatal(err)
				}
				candidate.Prepared.Effects = preparedEffects{record}
				if _, err := newProcessSnapshot(candidate); err != nil {
					t.Fatalf("valid wait rejected: %v", err)
				}
				status, payload := SettlementStatusSucceeded, record.Settlement.Payload()
				if mutation == "status" {
					status = SettlementStatusFailed
				} else {
					payload = []byte(`{"forged":true}`)
				}
				candidate.Prepared.Effects[0].Settlement = new(controlValue(NewSettlement(record.ID, status, payload)))
				encoded := controlValue(json.Marshal(candidate))
				if _, err := ParseProcessSnapshot(encoded); !errors.Is(err, ErrInvalidSnapshot) {
					t.Fatalf("forged wait settlement accepted: %v", err)
				}
			})
		}
	}
}

func TestSuccessfulChildStartRequiresCapturedChild(t *testing.T) {
	wire := controlValue(preparedEngineTestSnapshot(t).wire())
	deployment := newChildTestDeployment(t)
	spec := ChildSpec{Key: controlValue(ParseChildKey("child")), DeploymentRef: deployment.DeploymentRef(), Input: controlValue(EncodeInput(childTestInput{Mode: "leaf_pause"})), Budget: Budget{Steps: 1, Effects: 1, Signals: 1}, Capabilities: CapabilitySet{}}
	effect := controlValue(StartChild(spec))
	record := preparedEffect{ID: wire.ProcessID.effectID(1, 0), Effect: effect, Phase: effectPhasePending}
	if err := record.settleChildStart(ChildStartResult{key: spec.Key, processID: record.ID.childProcessID(), deploymentRef: spec.DeploymentRef}); err != nil {
		t.Fatal(err)
	}
	wire.Prepared.Transition = controlValue(Continue(0, effect))
	wire.Prepared.Effects = preparedEffects{record}
	for _, mutation := range []string{"key", "deployment", "process", "status", "payload", "invalid identity"} {
		t.Run(mutation, func(t *testing.T) {
			candidate := wire.clone()
			result := controlValue(decodeChildStartResult(record.Settlement.Payload()))
			status := SettlementStatusSucceeded
			switch mutation {
			case "key":
				result.key = controlValue(ParseChildKey("different"))
			case "deployment":
				result.deploymentRef = wire.DeploymentRef
			case "process":
				result.processID = controlValue(ParseProcessID("process:other"))
			case "status":
				status = SettlementStatusFailed
			}
			payload := controlValue(encodeChildStartResult(result))
			if mutation == "payload" {
				payload = []byte(`{}`)
			}
			if mutation == "invalid identity" {
				payload = []byte(`{"operation":"start_child","key":"bad key"}`)
			}
			candidate.Prepared.Effects[0].Settlement = new(controlValue(NewSettlement(record.ID, status, payload)))
			_, err := ParseProcessSnapshot(controlValue(json.Marshal(candidate)))
			if !errors.Is(err, ErrInvalidSnapshot) {
				t.Fatalf("contradictory child start accepted: %v", err)
			}
			if mutation == "invalid identity" && !errors.Is(err, ErrInvalidIdentity) {
				t.Fatalf("child-start decode cause was lost: %v", err)
			}
		})
	}
	snapshot := controlValue(newProcessSnapshot(wire))
	if _, err := newTreeSnapshot(treeSnapshotWire{RootID: wire.ProcessID, ProcessSnapshots: []ProcessSnapshot{snapshot}}); !errors.Is(err, ErrInvalidTreeSnapshot) {
		t.Fatalf("successful start without a child accepted: %v", err)
	}
	execution, state, _, err := initializeExecution(deployment.Definition(), spec.Input)
	if err != nil {
		t.Fatal(err)
	}
	relation := childProcessRelation(record.ID.childProcessID(), rootProcessRelation(wire.ProcessID), spec.Key)
	handle := newProcessHandleState(relation, spec.DeploymentRef, spec.Budget, spec.Capabilities, wire.TreeLimits, wire.StartedAt, StatusRunning)
	handle.childRequestDigest = controlValue(spec.digest())
	child := newProcessState(handle, deployment, execution, state, wire.StartedAt, controlValue(spec.Budget.limits(1)))
	wire.ReservedBudget = spec.Budget
	parentSnapshot := controlValue(newProcessSnapshot(wire))
	childSnapshot := controlValue(child.capture())
	if _, err := newTreeSnapshot(treeSnapshotWire{RootID: wire.ProcessID, ProcessSnapshots: []ProcessSnapshot{parentSnapshot, childSnapshot}}); err != nil {
		t.Fatalf("matching child rejected: %v", err)
	}
	for _, mutation := range []string{"allocation", "deployment", "request"} {
		t.Run("child/"+mutation, func(t *testing.T) {
			parentWire := wire.clone()
			childWire := controlValue(childSnapshot.wire())
			switch mutation {
			case "allocation":
				childWire.Budget.Steps++
				parentWire.ReservedBudget.Steps++
			case "deployment":
				childWire.DeploymentRef = parentWire.DeploymentRef
			case "request":
				childWire.ChildRequestDigest = new(ComputeDigest([]byte("different input")))
			}
			tree := treeSnapshotWire{RootID: wire.ProcessID, ProcessSnapshots: []ProcessSnapshot{controlValue(newProcessSnapshot(parentWire)), controlValue(newProcessSnapshot(childWire))}}
			if _, err := newTreeSnapshot(tree); !errors.Is(err, ErrInvalidTreeSnapshot) {
				t.Fatalf("contradictory captured child accepted: %v", err)
			}
		})
	}
}

func TestRestoreRejectsWaitConflictBeforeDispatch(t *testing.T) {
	wire := controlValue(preparedEngineTestSnapshot(t).wire())
	wait := controlValue(RequestWait(controlValue(ParseWaitKey("duplicate")), []byte(`"answer"`)))
	effects := []Effect{wire.Prepared.Effects[0].Effect, wait, wait}
	wire.Prepared.Transition = controlValue(Continue(0, effects...))
	wire.Prepared.Effects = nil
	for index, effect := range effects {
		wire.Prepared.Effects = append(wire.Prepared.Effects, preparedEffect{ID: wire.ProcessID.effectID(1, index), Effect: effect, Phase: effectPhasePlanned})
	}
	wire.Counters.PreparedEffects = uint64(len(effects))
	snapshot := controlValue(newProcessSnapshot(wire))
	tree := controlValue(newTreeSnapshot(treeSnapshotWire{RootID: wire.ProcessID, ProcessSnapshots: []ProcessSnapshot{snapshot}}))
	dispatcher := &engineTestDispatcher{policy: ReplayPolicySameIdentity}
	deployment := engineTestDeployment(t, newEngineTestDefinition(t, "engine.effect", "effect"), dispatcher)
	engine := controlValue(NewEngine(EngineConfig{}))
	defer mustCloseEngine(t, engine)
	process, err := engine.RestoreTree(t.Context(), deployment, tree)
	if process != nil {
		mustAwait(t, process)
	}
	if process != nil || !errors.Is(err, ErrInvalidSnapshot) || dispatcher.calls.Load() != 0 {
		t.Fatalf("invalid restored wait batch reached dispatch: process=%v error=%v calls=%d", process != nil, err, dispatcher.calls.Load())
	}
}
