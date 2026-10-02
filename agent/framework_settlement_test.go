package agent

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"
	"time"
)

func TestChildOutcomeRequiresUsageFacts(t *testing.T) {
	result, terminal := completedEngineTestSnapshot(t).Result()
	if !terminal || result.Usage().CommittedSteps == 0 {
		t.Fatal("fixture did not retain committed work")
	}
	original := ChildOutcome{key: controlValue(ParseChildKey("child")), result: result, boundary: ChildWaitBoundaryResult}
	for _, usage := range []json.RawMessage{nil, []byte(`null`), []byte(`{}`)} {
		var fields map[string]json.RawMessage
		if err := jsonv2.Unmarshal(controlValue(jsonv2.Marshal(result.wire())), &fields); err != nil {
			t.Fatal(err)
		}
		if usage == nil {
			delete(fields, "usage")
		} else {
			fields["usage"] = usage
		}
		data := controlValue(jsonv2.Marshal(struct {
			Key      ChildKey          `json:"key"`
			Boundary ChildWaitBoundary `json:"boundary"`
			Result   json.RawMessage   `json:"result"`
		}{Key: original.Key(), Boundary: original.Boundary(), Result: controlValue(jsonv2.Marshal(fields))}))
		decoded := original
		if err := jsonv2.Unmarshal(data, &decoded); !errors.Is(err, ErrInvalidChildWait) {
			t.Errorf("child outcome accepted usage %s: %v", usage, err)
		}
		if decoded.Result().Usage() != result.Usage() {
			t.Error("rejected outcome replaced retained usage")
		}
	}
}

func TestFrameworkParsersRejectCallerOwnedSignals(t *testing.T) {
	deployment := newChildTestDeployment(t)
	effectID := controlValue(ParseProcessID("process:framework")).effectID(1, 0)
	childID := effectID.childProcessID()
	key := controlValue(ParseChildKey("child"))
	waitID := effectID.waitID()
	spec := ChildWaitSpec{Key: controlValue(ParseWaitKey("children")), Children: []ProcessID{childID}, Boundary: ChildWaitBoundaryDrained, Condition: AllChildren()}
	start := ChildStartResult{key: key, processID: childID, deploymentRef: deployment.DeploymentRef()}
	control := ChildControlResult{childID: childID, operation: frameworkOperationCancelChild}
	failure := controlValue(NewFailure(FailureKindExecution, "test.failed", "test failure"))
	now := time.Now().UTC()
	result := Result{processID: childID, startedAt: now, finishedAt: now, termination: failure.termination()}
	completed := controlValue(encodeChildWaitSatisfied(waitID, spec.Key, spec.Boundary, []ChildOutcome{{key: key, result: result, boundary: spec.Boundary, subtreeUnresolvedEffects: []UnresolvedEffect{}}}))
	for _, test := range []struct {
		name   string
		signal Signal
		parse  func(Signal) error
	}{
		{"start", controlValue(NewSignal(effectID.settlementSignalID(), WaitID{}, controlValue(start.MarshalJSON()))), func(signal Signal) error { _, err := ParseChildStartResult(signal); return err }},
		{"control", controlValue(NewSignal(effectID.settlementSignalID(), WaitID{}, controlValue(jsonv2.Marshal(control)))), func(signal Signal) error { _, err := ParseChildControlResult(signal); return err }},
		{"opening", controlValue(NewSignal(effectID.settlementSignalID(), waitID, controlValue(encodeChildWaitOpened(spec)))), func(signal Signal) error { _, err := ParseChildWaitOpened(signal); return err }},
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
		controlValue(NewWaitEffect(key, []byte(`{"prompt":"original"}`))),
		controlValue(NewChildWaitEffect(ChildWaitSpec{Key: key, Children: []ProcessID{childID}, Boundary: ChildWaitBoundaryDrained, Condition: AllChildren()})),
	} {
		for _, mutation := range []string{"status", "payload"} {
			t.Run(string(effect.Payload())+"/"+mutation, func(t *testing.T) {
				candidate := wire.clone()
				candidate.Prepared.Intent = controlValue(Continue(0))
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
				encoded := controlValue(jsonv2.Marshal(candidate))
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
	spec := ChildSpec{Key: controlValue(ParseChildKey("child")), DeploymentRef: deployment.DeploymentRef(), Input: controlValue(EncodePayload(childTestInput{Mode: "leaf_pause"})), Budget: Budget{Steps: NewQuota(1), Effects: NewQuota(1), Signals: NewQuota(1)}, Capabilities: CapabilitySet{}}
	effect := controlValue(NewChildStartEffect(spec))
	record := preparedEffect{ID: wire.ProcessID.effectID(1, 0), Effect: effect, Phase: effectPhasePending}
	if err := record.settleChildStart(ChildStartResult{key: spec.Key, processID: record.ID.childProcessID(), deploymentRef: spec.DeploymentRef}); err != nil {
		t.Fatal(err)
	}
	wire.Prepared.Intent = controlValue(Continue(0))
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
			payload := controlValue(result.MarshalJSON())
			if mutation == "payload" {
				payload = []byte(`{}`)
			}
			if mutation == "invalid identity" {
				payload = []byte(`{"operation":"start_child","key":"bad key"}`)
			}
			candidate.Prepared.Effects[0].Settlement = new(controlValue(NewSettlement(record.ID, status, payload)))
			_, err := ParseProcessSnapshot(controlValue(jsonv2.Marshal(candidate)))
			if !errors.Is(err, ErrInvalidSnapshot) {
				t.Fatalf("contradictory child start accepted: %v", err)
			}
			if mutation == "invalid identity" && !errors.Is(err, ErrInvalidIdentity) {
				t.Fatalf("child-start decode cause was lost: %v", err)
			}
		})
	}
	snapshot := controlValue(newProcessSnapshot(wire))
	if _, err := newTreeSnapshot(treeSnapshotWire{TreeLimits: DefaultTreeLimits(), IncarnationID: newTreeIncarnationID(), RootID: wire.ProcessID, ProcessSnapshots: []ProcessSnapshot{snapshot}}); !errors.Is(err, ErrInvalidTreeSnapshot) {
		t.Fatalf("successful start without a child accepted: %v", err)
	}
	execution, state, _, err := initializeExecution(t.Context(), deployment.Definition(), spec.Input)
	if err != nil {
		t.Fatal(err)
	}
	relation := childProcessRelation(record.ID.childProcessID(), rootProcessRelation(wire.ProcessID), spec.Key)
	handle := newProcessHandle(relation, deployment, controlValue(spec.digest()), spec.Budget, spec.Capabilities, wire.StartedAt)
	child := newProcessState(handle, execution, state)
	parentSnapshot := controlValue(newProcessSnapshot(wire))
	childSnapshot := controlValue(child.capture())
	if _, err := newTreeSnapshot(treeSnapshotWire{TreeLimits: DefaultTreeLimits(), IncarnationID: newTreeIncarnationID(), RootID: wire.ProcessID, ProcessSnapshots: []ProcessSnapshot{parentSnapshot, childSnapshot}}); err != nil {
		t.Fatalf("matching child rejected: %v", err)
	}
	for _, mutation := range []string{"allocation", "deployment", "request"} {
		t.Run("child/"+mutation, func(t *testing.T) {
			parentWire := wire.clone()
			childWire := controlValue(childSnapshot.wire())
			switch mutation {
			case "allocation":
				childWire.Budget.Steps = NewQuota(childWire.Budget.Steps.maximum + 1)
			case "deployment":
				childWire.DeploymentRef = parentWire.DeploymentRef
			case "request":
				childWire.ChildRequestDigest = new(ComputeDigest([]byte("different input")))
			}
			tree := treeSnapshotWire{TreeLimits: DefaultTreeLimits(), IncarnationID: newTreeIncarnationID(), RootID: wire.ProcessID, ProcessSnapshots: []ProcessSnapshot{controlValue(newProcessSnapshot(parentWire)), controlValue(newProcessSnapshot(childWire))}}
			if _, err := newTreeSnapshot(tree); !errors.Is(err, ErrInvalidTreeSnapshot) {
				t.Fatalf("contradictory captured child accepted: %v", err)
			}
		})
	}
}

func TestRestoreRejectsWaitConflictBeforeDispatch(t *testing.T) {
	wire := controlValue(preparedEngineTestSnapshot(t).wire())
	wait := controlValue(NewWaitEffect(controlValue(ParseWaitKey("duplicate")), []byte(`"answer"`)))
	effects := []Effect{wire.Prepared.Effects[0].Effect, wait, wait}
	wire.Prepared.Intent = controlValue(Continue(0))
	wire.Prepared.Effects = nil
	for index, effect := range effects {
		wire.Prepared.Effects = append(wire.Prepared.Effects, preparedEffect{ID: wire.ProcessID.effectID(1, index), Effect: effect, Phase: effectPhasePlanned})
	}
	wire.Counters.PreparedEffects = uint64(len(effects))
	snapshot := controlValue(newProcessSnapshot(wire))
	tree := controlValue(newTreeSnapshot(treeSnapshotWire{TreeLimits: DefaultTreeLimits(), IncarnationID: newTreeIncarnationID(), RootID: wire.ProcessID, ProcessSnapshots: []ProcessSnapshot{snapshot}}))
	dispatcher := &engineTestDispatcher{policy: ReplayPolicySameIdentity}
	deployment := engineTestDeployment(t, newEngineTestDefinition(t, "engine.effect", "effect"), dispatcher)
	engine := controlValue(NewEngine(EngineConfig{TreeCommitter: newSnapshotTestCommitter(tree)}))
	defer mustCloseEngine(t, engine)
	process, err := engine.RestoreTree(t.Context(), deployment, tree)
	if process != nil {
		mustAwait(t, process)
	}
	if process != nil || !errors.Is(err, ErrInvalidSnapshot) || dispatcher.calls.Load() != 0 {
		t.Fatalf("invalid restored wait batch reached dispatch: process=%v error=%v calls=%d", process != nil, err, dispatcher.calls.Load())
	}
}

func TestChildOutcomeBoundarySurvivesEmptySubtreeRoundTrip(t *testing.T) {
	id := controlValue(ParseProcessID("child"))
	now := time.Unix(1, 0).UTC()
	failure := controlValue(NewFailure(FailureKindExecution, "test.failed", "failed"))
	result := Result{processID: id, startedAt: now, finishedAt: now, termination: failure.termination()}
	for _, boundary := range []ChildWaitBoundary{ChildWaitBoundaryResult, ChildWaitBoundaryDrained} {
		for _, effects := range [][]UnresolvedEffect{nil, {}} {
			outcome := ChildOutcome{key: controlValue(ParseChildKey("child")), result: result, boundary: boundary, subtreeUnresolvedEffects: effects}
			data := controlValue(jsonv2.Marshal(outcome))
			var restored ChildOutcome
			if err := jsonv2.Unmarshal(data, &restored); err != nil {
				t.Fatal(err)
			}
			unresolved, known := restored.SubtreeUnresolvedEffects()
			if !restored.Valid() || restored.Boundary() != boundary || len(unresolved) != 0 || known != (boundary == ChildWaitBoundaryDrained) {
				t.Fatalf("lost boundary: %s, known=%t", data, known)
			}
			if restored.SubtreeResolved() != (boundary == ChildWaitBoundaryDrained) {
				t.Fatalf("%s outcome with an empty subtree reports resolved=%t", boundary, restored.SubtreeResolved())
			}
			wire := outcome.wire()
			wire.Boundary = ChildWaitBoundaryInvalid
			if err := jsonv2.Unmarshal(controlValue(jsonv2.Marshal(wire)), &restored); !errors.Is(err, ErrInvalidChildWait) {
				t.Fatalf("outcome without its own boundary accepted: %v", err)
			}
			if restored.Boundary() != boundary {
				t.Fatal("rejected decode changed the outcome")
			}
		}
	}
	outcome := ChildOutcome{key: controlValue(ParseChildKey("child")), result: result, boundary: ChildWaitBoundaryResult,
		subtreeUnresolvedEffects: []UnresolvedEffect{{ProcessID: id, EffectID: id.effectID(1, 0)}}}
	if outcome.Valid() {
		t.Fatal("result boundary accepted subtree evidence")
	}
	descendant := controlValue(ParseProcessID("grandchild"))
	outcome.boundary = ChildWaitBoundaryDrained
	outcome.subtreeUnresolvedEffects = []UnresolvedEffect{{ProcessID: descendant, EffectID: descendant.effectID(1, 0)}}
	if !outcome.Valid() || outcome.SubtreeResolved() {
		t.Fatal("a drained subtree with retained Unknown settlements reported resolved")
	}
}
