package agent

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestChildOutcomeRequiresUsageFacts(t *testing.T) {
	result, terminal := completedEngineTestSnapshot(t).Result()
	if !terminal || result.Usage().CommittedSteps == 0 {
		t.Fatal("fixture did not retain committed work")
	}
	original := ChildOutcome{result: result}
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
			Result json.RawMessage `json:"result"`
		}{Result: controlValue(jsonv2.Marshal(fields))}))
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
	effectID := controlValue(ParseProcessID("process:framework")).effectID(1, 0)
	childID := effectID.childProcessID()
	waitID := effectID.waitID()
	start := ChildStartResult{processID: childID}
	var control ChildControlResult
	failure := controlValue(NewFailure(FailureKindExecution, "test.failed", "test failure"))
	now := time.Now().UTC()
	result := Result{processID: childID, startedAt: now, finishedAt: now, termination: failure.termination()}
	completed := controlValue(encodeChildWaitSatisfied(waitID, []ChildOutcome{{result: result, descendantUnresolvedEffects: new([]UnresolvedEffect{})}}))
	for _, test := range []struct {
		name   string
		signal Signal
		parse  func(Signal) error
	}{
		{"start", controlValue(NewSignal(effectID.settlementSignalID(), WaitID{}, controlValue(start.MarshalJSON()))), func(signal Signal) error { _, err := ParseChildStartResult(signal); return err }},
		{"control", controlValue(NewSignal(effectID.settlementSignalID(), WaitID{}, controlValue(jsonv2.Marshal(control)))), func(signal Signal) error { _, err := ParseChildControlResult(signal); return err }},
		{"opening", controlValue(NewSignal(effectID.settlementSignalID(), waitID, childWaitOpenedPayload())), func(signal Signal) error { _, err := ParseChildWaitOpened(signal); return err }},
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

func TestWaitSettlementFollowsDeclaredRequest(t *testing.T) {
	wire := controlValue(preparedEngineTestSnapshot(t).wire())
	key := controlValue(ParseWaitKey("wait"))
	childID := controlValue(ParseProcessID("process:child"))
	for _, effect := range []Effect{
		controlValue(NewWaitEffect(key)),
		controlValue(NewChildWaitEffect(ChildWaitSpec{Key: key, Children: []ProcessID{childID}, Boundary: ChildWaitBoundaryDrained, Condition: AllChildren()})),
	} {
		t.Run(string(effect.Payload()), func(t *testing.T) {
			candidate := wire.clone()
			candidate.Prepared.Intent = controlValue(Continue(0))
			record := preparedEffect{ID: candidate.processID().effectID(1, 0), Effect: effect, progress: &effectProgress{}}
			if err := settleTestFramework(&record, Failure{}); err != nil {
				t.Fatal(err)
			}
			candidate.Prepared.Effects = preparedEffects{record}
			snapshot, err := newProcessSnapshot(candidate)
			if err != nil {
				t.Fatalf("valid wait rejected: %v", err)
			}
			restored := controlValue(controlValue(parseTestProcessSnapshot(snapshot.JSON())).wire())
			if settlement := restored.Prepared.Effects[0].settlement(); settlement == nil || !settlement.equal(*record.settlement()) {
				t.Fatalf("restored wait settlement = %+v, want %+v", settlement, record.settlement())
			}
			var fields map[string]json.RawMessage
			if err := jsonv2.Unmarshal(snapshot.JSON(), &fields); err != nil {
				t.Fatal(err)
			}
			var prepared map[string]json.RawMessage
			if err := jsonv2.Unmarshal(fields["prepared"], &prepared); err != nil {
				t.Fatal(err)
			}
			var effects []map[string]json.RawMessage
			if err := jsonv2.Unmarshal(prepared["effects"], &effects); err != nil {
				t.Fatal(err)
			}
			if _, stored := effects[0]["settlement"]; stored {
				t.Fatal("wait settlement was encoded beside the request that determines it")
			}
			effects[0]["settlement"] = json.RawMessage(`{"status":"succeeded","payload":{"forged":true}}`)
			prepared["effects"] = controlValue(jsonv2.Marshal(effects))
			fields["prepared"] = controlValue(jsonv2.Marshal(prepared))
			if _, err := parseTestProcessSnapshot(controlValue(jsonv2.Marshal(fields))); !errors.Is(err, ErrInvalidSnapshot) {
				t.Fatalf("stored wait settlement accepted: %v", err)
			}
		})
	}
}

func TestSuccessfulChildStartRequiresCapturedChild(t *testing.T) {
	wire := controlValue(preparedEngineTestSnapshot(t).wire())
	deployment := newChildTestDeployment(t)
	spec := ChildSpec{Key: controlValue(ParseChildKey("child")), DeploymentRef: deployment.DeploymentRef(), Input: controlValue(EncodePayload(childTestInput{Mode: "leaf_pause"})), Budget: Budget{Steps: NewQuota(1), Effects: NewQuota(1), Signals: NewQuota(1)}, Capabilities: CapabilitySet{}}
	effect := controlValue(NewChildStartEffect(spec))
	record := preparedEffect{ID: wire.processID().effectID(1, 0), Effect: effect, progress: &effectProgress{}}
	if err := settleTestFramework(&record, Failure{}); err != nil {
		t.Fatal(err)
	}
	wire.Prepared.Intent = controlValue(Continue(0))
	wire.Prepared.Effects = preparedEffects{record}
	stored := controlValue(record.wire())
	if stored.Progress.Settlement.Status != SettlementStatusInvalid || stored.Progress.Settlement.Payload != nil {
		t.Fatalf("child start stored settlement facts its request determines: %+v", stored.Progress.Settlement)
	}
	if stored.Effect != nil || stored.StartedChild == nil || !bytes.Equal(stored.StartedChild.Input.JSON(), spec.Input.JSON()) {
		t.Fatalf("started child stored more than its input: %+v", stored)
	}
	grant := spec
	grant.Input = Payload{}
	grants := func(ProcessID) (ChildSpec, error) { return grant, nil }
	if restored := controlValue(stored.record(record.ID, grants)); !restored.Effect.equal(effect) {
		t.Fatal("started child request did not decode from its child's grant")
	}
	withGrant := stored
	withGrant.Effect, withGrant.StartedChild = &effect, nil
	if _, err := withGrant.record(record.ID, grants); err == nil {
		t.Fatal("started child accepted a stored copy of its child's grant")
	}
	stored.Progress.Settlement = &preparedSettlementWire{Status: SettlementStatusSucceeded, Payload: record.settlement().Payload()}
	if _, err := stored.record(record.ID, grants); err == nil {
		t.Fatal("child start accepted a stored copy of its request")
	}
	snapshot := controlValue(newProcessSnapshot(wire))
	if _, err := newTreeSnapshot(treeSnapshotWire{TreeLimits: DefaultTreeLimits(), IncarnationID: newTreeIncarnationID(), ProcessSnapshots: []ProcessSnapshot{snapshot}}); !errors.Is(err, ErrInvalidTreeSnapshot) {
		t.Fatalf("successful start without a child accepted: %v", err)
	}
	execution, state, _, err := initializeExecution(t.Context(), deployment.Definition(), spec.Input)
	if err != nil {
		t.Fatal(err)
	}
	relation := childProcessRelation(record.ID.childProcessID(), rootProcessRelation(wire.processID()), spec.Key)
	handle := newProcessHandle(relation, deployment, spec.Budget, spec.Capabilities, wire.StartedAt)
	child := newProcessState(handle, execution, state)
	parentSnapshot := controlValue(newProcessSnapshot(wire))
	childSnapshot := controlValue(child.capture())
	if _, err := newTreeSnapshot(treeSnapshotWire{TreeLimits: DefaultTreeLimits(), IncarnationID: newTreeIncarnationID(), ProcessSnapshots: []ProcessSnapshot{parentSnapshot, childSnapshot}}); err != nil {
		t.Fatalf("matching child rejected: %v", err)
	}
	t.Run("child owns its grant", func(t *testing.T) {
		childWire := controlValue(childSnapshot.wire())
		childWire.Budget.Steps = NewQuota(childWire.Budget.Steps.maximum + 1)
		tree := controlValue(newTreeSnapshot(treeSnapshotWire{TreeLimits: DefaultTreeLimits(), IncarnationID: newTreeIncarnationID(), ProcessSnapshots: []ProcessSnapshot{parentSnapshot, controlValue(newProcessSnapshot(childWire))}}))
		parsed := controlValue(ParseTreeSnapshot(tree.JSON()))
		request, found := parsed.EffectRequest(wire.processID(), record.ID)
		if !found {
			t.Fatal("started child request is missing")
		}
		granted := controlValue(ParseChildStartEffect(request.Effect()))
		if granted.Budget != childWire.Budget || !bytes.Equal(granted.Input.JSON(), spec.Input.JSON()) {
			t.Fatalf("decoded request = %+v, want the child's grant with the parent's input", granted)
		}
	})
	t.Run("child before settlement", func(t *testing.T) {
		parentWire := wire.clone()
		parentWire.Prepared.Effects = preparedEffects{{ID: record.ID, Effect: effect, progress: &effectProgress{}}}
		tree := treeSnapshotWire{TreeLimits: DefaultTreeLimits(), IncarnationID: newTreeIncarnationID(), ProcessSnapshots: []ProcessSnapshot{controlValue(newProcessSnapshot(parentWire)), childSnapshot}}
		if _, err := newTreeSnapshot(tree); !errors.Is(err, ErrInvalidTreeSnapshot) || !strings.Contains(err.Error(), "before its start settled") {
			t.Fatalf("child published before its start settled was accepted: %v", err)
		}
	})
}

func TestRestoreRejectsWaitConflictBeforeDispatch(t *testing.T) {
	wire := controlValue(preparedEngineTestSnapshot(t).wire())
	wait := controlValue(NewWaitEffect(controlValue(ParseWaitKey("duplicate"))))
	effects := []Effect{wire.Prepared.Effects[0].Effect, wait, wait}
	wire.Prepared.Intent = controlValue(Continue(0))
	wire.Prepared.Effects = nil
	for index, effect := range effects {
		wire.Prepared.Effects = append(wire.Prepared.Effects, preparedEffect{ID: wire.processID().effectID(1, index), Effect: effect})
	}
	wire.Counters.PreparedEffects = uint64(len(effects))
	snapshot := controlValue(newProcessSnapshot(wire))
	tree := controlValue(newTreeSnapshot(treeSnapshotWire{TreeLimits: DefaultTreeLimits(), IncarnationID: newTreeIncarnationID(), ProcessSnapshots: []ProcessSnapshot{snapshot}}))
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

func TestChildOutcomeDrainedSubtreeSurvivesEmptyRoundTrip(t *testing.T) {
	id := controlValue(ParseProcessID("child"))
	now := time.Unix(1, 0).UTC()
	failure := controlValue(NewFailure(FailureKindExecution, "test.failed", "failed"))
	result := Result{processID: id, startedAt: now, finishedAt: now, termination: failure.termination()}
	for _, drained := range []bool{false, true} {
		outcome := ChildOutcome{result: result}
		if drained {
			outcome.descendantUnresolvedEffects = new([]UnresolvedEffect{})
		}
		data := controlValue(jsonv2.Marshal(outcome))
		var restored ChildOutcome
		if err := jsonv2.Unmarshal(data, &restored); err != nil {
			t.Fatal(err)
		}
		unresolved, known := restored.SubtreeUnresolvedEffects()
		if !restored.Valid() || len(unresolved) != 0 || known != drained {
			t.Fatalf("lost drained subtree: %s, known=%t", data, known)
		}
		if restored.SubtreeResolved() != drained {
			t.Fatalf("drained=%t outcome with an empty subtree reports resolved=%t", drained, restored.SubtreeResolved())
		}
	}
	descendant := controlValue(ParseProcessID("grandchild"))
	outcome := ChildOutcome{result: result, descendantUnresolvedEffects: new([]UnresolvedEffect{{ProcessID: descendant, EffectID: descendant.effectID(1, 0)}})}
	if !outcome.Valid() || outcome.SubtreeResolved() {
		t.Fatal("a drained subtree with retained Unknown settlements reported resolved")
	}
	outcome.descendantUnresolvedEffects = new([]UnresolvedEffect{{ProcessID: id, EffectID: id.effectID(1, 0)}})
	if outcome.Valid() {
		t.Fatal("descendant evidence repeated the child's own termination")
	}
}

func TestChildOutcomeRendersOwnUnresolvedEffectsFromTermination(t *testing.T) {
	id := controlValue(ParseProcessID("child"))
	descendant := controlValue(ParseProcessID("grandchild"))
	now := time.Unix(1, 0).UTC()
	failure := controlValue(NewFailure(FailureKindExecution, "test.failed", "failed"))
	outcome := ChildOutcome{
		result: Result{
			processID: id, startedAt: now, finishedAt: now,
			termination: failure.termination().withUnresolvedEffectIDs([]EffectID{id.effectID(1, 0)}),
		},
		descendantUnresolvedEffects: new([]UnresolvedEffect{{ProcessID: descendant, EffectID: descendant.effectID(1, 0)}}),
	}
	data := controlValue(jsonv2.Marshal(outcome))
	if strings.Count(string(data), id.effectID(1, 0).String()) != 1 {
		t.Fatalf("own unresolved Effect is not stored exactly once: %s", data)
	}
	var restored ChildOutcome
	if err := jsonv2.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	want := []UnresolvedEffect{{ProcessID: id, EffectID: id.effectID(1, 0)}, {ProcessID: descendant, EffectID: descendant.effectID(1, 0)}}
	slices.SortFunc(want, UnresolvedEffect.compare)
	if got, drained := restored.SubtreeUnresolvedEffects(); !drained || !slices.Equal(got, want) || restored.SubtreeResolved() {
		t.Fatalf("subtree projection = %v, drained=%t; want %v", got, drained, want)
	}
}

// settleTestFramework settles a Framework record as the runtime does: waits
// settle as they begin, and other operations add only an optional failure.
func settleTestFramework(record *preparedEffect, failure Failure) error {
	operation, err := decodeFrameworkOperation(record.Effect.Payload())
	if err != nil {
		return err
	}
	switch operation.(type) {
	case waitOperation, childWaitOperation:
		return record.settleLocally(operation)
	default:
		return record.settleOperation(operation, failure)
	}
}

func TestChildWaitSatisfiedMatchesObservedEvidenceToItsWait(t *testing.T) {
	effectID := controlValue(ParseProcessID("process:satisfied")).effectID(1, 0)
	waitID, childID := effectID.waitID(), effectID.childProcessID()
	now := time.Unix(1, 0).UTC()
	failure := controlValue(NewFailure(FailureKindExecution, "test.failed", "failed"))
	result := Result{processID: childID, startedAt: now, finishedAt: now, termination: failure.termination()}
	key := controlValue(ParseWaitKey("children"))
	for _, drained := range []bool{false, true} {
		outcome := ChildOutcome{result: result}
		if drained {
			outcome.descendantUnresolvedEffects = new([]UnresolvedEffect{})
		}
		satisfied := controlValue(ParseChildWaitSatisfied(controlValue(encodeChildWaitSatisfied(waitID, []ChildOutcome{outcome}))))
		for _, boundary := range []ChildWaitBoundary{ChildWaitBoundaryResult, ChildWaitBoundaryDrained} {
			spec := ChildWaitSpec{Key: key, Children: []ProcessID{childID}, Boundary: boundary, Condition: AllChildren()}
			if got, want := satisfied.Matches(waitID, spec), drained == (boundary == ChildWaitBoundaryDrained); got != want {
				t.Fatalf("drained=%t boundary=%s matches=%t, want %t", drained, boundary, got, want)
			}
		}
	}
}
