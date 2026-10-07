package agent

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"maps"
	"math"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

// newProcessSnapshot validates a copy of wire, so tests may keep editing the
// fixture they captured it from.
func newProcessSnapshot(wire processSnapshotWire) (ProcessSnapshot, error) {
	return processSnapshotFromWire(wire.clone())
}

func TestSnapshotStrictlyRejectsUnknownFields(t *testing.T) {
	snapshot := completedEngineTestSnapshot(t)
	var fields map[string]json.RawMessage
	if err := jsonv2.Unmarshal(snapshot.JSON(), &fields); err != nil {
		t.Fatal(err)
	}
	fields["application_revision"] = json.RawMessage(`1`)
	data, err := jsonv2.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseTestProcessSnapshot(data); err == nil {
		t.Fatal("ParseSnapshot accepted an unknown application field")
	}
}

func TestSnapshotCanonicalizesRecordedInstants(t *testing.T) {
	snapshot := completedEngineTestSnapshot(t)
	var fields map[string]json.RawMessage
	if err := jsonv2.Unmarshal(snapshot.JSON(), &fields); err != nil {
		t.Fatal(err)
	}
	var finish map[string]json.RawMessage
	if err := jsonv2.Unmarshal(fields["finish"], &finish); err != nil {
		t.Fatal(err)
	}
	offset := time.FixedZone("offset", 8*60*60)
	for _, member := range []struct {
		fields map[string]json.RawMessage
		name   string
	}{{fields, "started_at"}, {finish, "finished_at"}} {
		var instant time.Time
		if err := jsonv2.Unmarshal(member.fields[member.name], &instant); err != nil {
			t.Fatal(err)
		}
		member.fields[member.name] = controlValue(jsonv2.Marshal(instant.In(offset)))
	}
	fields["finish"] = controlValue(jsonv2.Marshal(finish))
	rendered := controlValue(jsonv2.Marshal(fields))
	if bytes.Equal(rendered, snapshot.JSON()) {
		t.Fatal("fixture did not change the rendered offset")
	}
	parsed, err := parseTestProcessSnapshot(rendered)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(parsed.JSON(), snapshot.JSON()) {
		t.Fatalf("an offset rendering changed the canonical snapshot:\n%s\n%s", parsed.JSON(), snapshot.JSON())
	}
}

func TestProcessSnapshotOwnsMutableWire(t *testing.T) {
	prepared, err := preparedEngineTestSnapshot(t).wire()
	if err != nil {
		t.Fatal(err)
	}
	id, _ := ParseSignalID("signal:snapshot-ownership")
	signal, err := NewSignal(id, WaitID{}, []byte(`{"value":"retained"}`))
	if err != nil {
		t.Fatal(err)
	}
	prepared.Mailbox.Signals = []signalRecordWire{newSignalRecord(signal).wire()}
	failure, _ := NewFailure(FailureKindContract, "test.pending.failure", "pending failure")
	prepared.PendingControl.Failure = &failure
	completed, err := completedEngineTestSnapshot(t).wire()
	if err != nil {
		t.Fatal(err)
	}
	for _, wire := range []processSnapshotWire{prepared, completed} {
		t.Run(controlValue(newProcessSnapshot(wire)).Status().String(), func(t *testing.T) {
			snapshot, err := newProcessSnapshot(wire)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := parseTestProcessSnapshot(snapshot.JSON())
			if err != nil || !bytes.Equal(snapshot.JSON(), parsed.JSON()) {
				t.Fatalf("structured construction and parsing disagree: %v", err)
			}
			mutate := func(value processSnapshotWire) {
				value.Mailbox.Signals[0].ID = SignalID{}
				if len(value.Mailbox.Signals[0].Payload) > 0 {
					value.Mailbox.Signals[0].Payload[0] = '['
				}
				if value.Prepared != nil {
					value.Prepared.Effects[0].ID = EffectID{}
					*value.PendingControl.Failure = Failure{}
				}
				if value.Finish != nil {
					value.Finish.FinishedAt = value.StartedAt
					value.Finish.Termination = Termination{}
				}
			}
			mutate(wire)
			returned, err := snapshot.wire()
			if err != nil {
				t.Fatal(err)
			}
			mutate(returned)
			retained, err := snapshot.wire()
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := jsonv2.Marshal(retained)
			if err != nil || !bytes.Equal(encoded, parsed.JSON()) {
				t.Fatalf("wire mutation changed immutable snapshot facts: %v", err)
			}
			if receipts := snapshot.SignalReceipts(); receipts[0].ID() != parsed.SignalReceipts()[0].ID() {
				t.Fatal("wire mutation changed public admission receipts")
			}
		})
	}
}

func TestPreparedSnapshotPreservesCommittedExecutionState(t *testing.T) {
	snapshot := preparedEngineTestSnapshot(t)
	var projection struct {
		CommittedState ExecutionState `json:"committed_execution_state"`
		Prepared       struct {
			CandidateState ExecutionState `json:"candidate_state"`
		} `json:"prepared"`
	}
	if err := jsonv2.Unmarshal(snapshot.JSON(), &projection); err != nil {
		t.Fatal(err)
	}
	committed := controlValue(jsonv2.Marshal(snapshot.CommittedExecutionState()))
	encoded := controlValue(jsonv2.Marshal(projection.CommittedState))
	candidate := controlValue(jsonv2.Marshal(projection.Prepared.CandidateState))
	if !bytes.Equal(committed, encoded) || bytes.Equal(committed, candidate) {
		t.Fatal("prepared snapshot did not preserve the committed state beside its candidate")
	}
}

func TestPreparedSnapshotRejectsCopiedProgress(t *testing.T) {
	snapshot := preparedEngineTestSnapshot(t)
	for name, value := range map[string]json.RawMessage{
		"step_sequence":                    json.RawMessage(`1`),
		"committed_execution_state_digest": json.RawMessage(`"` + ComputeDigest(nil).String() + `"`),
	} {
		t.Run(name, func(t *testing.T) {
			var wire map[string]json.RawMessage
			if err := jsonv2.Unmarshal(snapshot.JSON(), &wire); err != nil {
				t.Fatal(err)
			}
			var prepared map[string]json.RawMessage
			if err := jsonv2.Unmarshal(wire["prepared"], &prepared); err != nil {
				t.Fatal(err)
			}
			prepared[name] = value
			wire["prepared"] = controlValue(jsonv2.Marshal(prepared))
			if _, err := parseTestProcessSnapshot(controlValue(jsonv2.Marshal(wire))); !errors.Is(err, ErrInvalidSnapshot) {
				t.Fatalf("copied prepared progress accepted: %v", err)
			}
		})
	}
}

func TestPreparedConsumptionUsesOnlyPendingSignals(t *testing.T) {
	wire, err := preparedEngineTestSnapshot(t).wire()
	if err != nil {
		t.Fatal(err)
	}
	mailbox := newSignalMailbox()
	for _, name := range []string{"signal:consumed", "signal:pending"} {
		id := controlValue(ParseSignalID(name))
		signal := controlValue(NewSignal(id, WaitID{}, []byte(`"input"`)))
		if accepted, enqueueErr := mailbox.enqueue(StatusRunning, signal, signalSourceExternal); enqueueErr != nil || !accepted {
			t.Fatalf("signal admission = %t, %v", accepted, enqueueErr)
		}
	}
	if err = mailbox.commit(1); err != nil {
		t.Fatal(err)
	}
	wire.Mailbox = mailbox.wire()
	wire.CommittedSteps++
	for index := range wire.Prepared.Effects {
		wire.Prepared.Effects[index].ID = wire.processID().effectID(wire.CommittedSteps+1, index)
	}
	for _, consumed := range []uint32{0, 1, 2, math.MaxUint32} {
		wire.Prepared.Intent = controlValue(Continue(consumed))
		data := controlValue(jsonv2.Marshal(wire))
		snapshot, parseErr := parseTestProcessSnapshot(data)
		if consumed > 1 {
			if !errors.Is(parseErr, ErrInvalidSnapshot) {
				t.Fatalf("consumption %d exceeds the pending suffix: %v", consumed, parseErr)
			}
			continue
		}
		if parseErr != nil {
			t.Fatalf("valid consumption %d rejected: %v", consumed, parseErr)
		}
		receipts := snapshot.SignalReceipts()
		if len(receipts) != 2 || !receipts[0].Consumed() || receipts[1].Consumed() {
			t.Fatal("prepared consumption changed committed Signal receipts")
		}
	}
}

func TestSnapshotRejectsRetiredUsageRepresentation(t *testing.T) {
	snapshot := completedEngineTestSnapshot(t)
	var wire map[string]json.RawMessage
	if err := jsonv2.Unmarshal(snapshot.JSON(), &wire); err != nil {
		t.Fatal(err)
	}
	wire["usage"] = json.RawMessage(`{"accepted_signals":0}`)
	data, err := jsonv2.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseTestProcessSnapshot(data); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("retired usage accepted: %v", err)
	}
}

func TestSnapshotRejectsPreparedStepSequenceOverflow(t *testing.T) {
	snapshot := preparedEngineTestSnapshot(t)
	wire, err := snapshot.wire()
	if err != nil {
		t.Fatal(err)
	}
	if wire.Prepared == nil || len(wire.Prepared.Effects) != 1 {
		t.Fatalf("prepared fixture = %#v", wire.Prepared)
	}
	wire.CommittedSteps = math.MaxUint64
	wire.Budget.Steps = NewQuota(math.MaxUint64)
	wire.Prepared.Effects[0].ID = wire.processID().effectID(0, 0)
	data, err := jsonv2.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseTestProcessSnapshot(data); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("prepared Step overflow error = %v, want ErrInvalidSnapshot", err)
	}
}

func TestSnapshotAccountsForPreparedEffectIdentities(t *testing.T) {
	snapshot := preparedEngineTestSnapshot(t)
	wire := controlValue(snapshot.wire())
	settled := wire.Mailbox.settlementCount()
	if got, want := snapshot.Usage().PreparedEffects, settled+uint64(len(wire.Prepared.Effects)); got != want || want == settled {
		t.Fatalf("prepared Effect usage = %d, want %d settled plus every prepared identity", got, want)
	}
	var fields map[string]json.RawMessage
	if err := jsonv2.Unmarshal(snapshot.JSON(), &fields); err != nil {
		t.Fatal(err)
	}
	if _, stored := fields["prepared_effects"]; stored {
		t.Fatal("prepared Effect usage is stored beside the records that own it")
	}
}

func TestPreparedEffectProgressOwnsMonotonicTransitions(t *testing.T) {
	snapshot := preparedEngineTestSnapshot(t)
	wire, err := snapshot.wire()
	if err != nil {
		t.Fatal(err)
	}
	record := &wire.Prepared.Effects[0]
	if record.phase() != effectPhasePlanned {
		t.Fatalf("initial phase = %s, want %s", record.phase(), effectPhasePlanned)
	}
	if beginErr := record.begin(); beginErr != nil || record.phase() != effectPhasePending {
		t.Fatalf("begin phase = %s, error = %v", record.phase(), beginErr)
	}
	settlement, err := NewSettlement(SettlementStatusUnknown, json.RawMessage(`null`))
	if err != nil {
		t.Fatal(err)
	}
	if settleErr := record.settle(settlement, nil); settleErr != nil || !record.unknown() {
		t.Fatalf("settle phase = %s, unknown = %t, error = %v", record.phase(), record.unknown(), settleErr)
	}
	definite, err := NewSettlement(SettlementStatusSucceeded, json.RawMessage(`{"ok":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if resolveErr := record.resolveUnknown(definite); resolveErr != nil || !record.definitelySettled() {
		t.Fatalf("resolve phase = %s, definite = %t, error = %v", record.phase(), record.definitelySettled(), resolveErr)
	}
	if beginErr := record.begin(); beginErr == nil {
		t.Fatal("settled Effect moved backward to pending")
	}
}

func TestPreparedEffectCandidatesOwnDispatchProgress(t *testing.T) {
	id := controlValue(ParseProcessID("process:progress-owner")).effectID(1, 0)
	effect := controlValue(NewDispatcherEffect([]byte(`{"request":"work"}`)))
	prepared := preparedStep{Effects: preparedEffects{{ID: id, Effect: effect, progress: &effectProgress{}}}}
	before := controlValue(jsonv2.Marshal(prepared.Effects))
	clone := prepared.clone()
	if err := clone.Effects[0].settleUnknown(); err != nil {
		t.Fatal(err)
	}
	if prepared.Effects[0].phase() != effectPhasePending || !clone.Effects[0].unknown() {
		t.Fatal("candidate settlement advanced its source dispatch permission")
	}
	if after := controlValue(jsonv2.Marshal(prepared.Effects)); !bytes.Equal(before, after) {
		t.Fatal("candidate changed the source wire progress")
	}
}

func TestPreparedEffectWireHasOneProgressRepresentation(t *testing.T) {
	wire, err := preparedEngineTestSnapshot(t).wire()
	if err != nil {
		t.Fatal(err)
	}
	record := &wire.Prepared.Effects[0]
	planned := controlValue(jsonv2.Marshal(controlValue(record.wire())))
	if bytes.Contains(planned, []byte(`"progress"`)) || bytes.Contains(planned, []byte(`"phase"`)) {
		t.Fatalf("planned effect stores progress: %s", planned)
	}
	if beginErr := record.begin(); beginErr != nil {
		t.Fatal(beginErr)
	}
	pending := controlValue(jsonv2.Marshal(controlValue(record.wire())))
	if !bytes.Contains(pending, []byte(`"progress":{}`)) {
		t.Fatalf("pending permission disappeared: %s", pending)
	}
	decoded := controlValue(jsonwire.Decode[preparedEffectWire](pending))
	if restored := controlValue(decoded.record(record.ID, nil)); restored.phase() != effectPhasePending {
		t.Fatalf("restored pending effect phase = %s", restored.phase())
	}
	legacy := append(bytes.TrimSuffix(planned, []byte(`}`)), []byte(`,"phase":"planned"}`)...)
	if _, err := jsonwire.Decode[preparedEffectWire](legacy); err == nil {
		t.Fatalf("accepted legacy phase: %s", legacy)
	}
}

func TestSnapshotEnforcesSequentialEffectProgress(t *testing.T) {
	base := preparedEngineTestSnapshot(t)
	type progress struct {
		phase      effectPhase
		settlement SettlementStatus
	}
	planned := progress{phase: effectPhasePlanned}
	pending := progress{phase: effectPhasePending}
	settled := progress{phase: effectPhaseSettled, settlement: SettlementStatusSucceeded}
	unknown := progress{phase: effectPhaseSettled, settlement: SettlementStatusUnknown}
	for _, sample := range []struct {
		name    string
		effects []progress
		valid   bool
	}{
		{name: "all planned", effects: []progress{planned, planned, planned}, valid: true},
		{name: "first pending", effects: []progress{pending, planned, planned}, valid: true},
		{name: "settled prefix", effects: []progress{settled, settled, planned}, valid: true},
		{name: "pending frontier", effects: []progress{settled, pending, planned}, valid: true},
		{name: "unknown frontier", effects: []progress{settled, unknown, planned}, valid: true},
		{name: "all settled", effects: []progress{settled, settled, settled}, valid: true},
		{name: "planned before pending", effects: []progress{planned, pending, planned}},
		{name: "multiple pending", effects: []progress{pending, pending, planned}},
		{name: "planned before settled", effects: []progress{planned, settled, planned}},
		{name: "unknown before settled", effects: []progress{unknown, settled, planned}},
		{name: "unknown before pending", effects: []progress{unknown, pending, planned}},
		{name: "multiple unknown", effects: []progress{unknown, unknown, planned}},
	} {
		t.Run(sample.name, func(t *testing.T) {
			wire, err := base.wire()
			if err != nil {
				t.Fatal(err)
			}
			effect := wire.Prepared.Effects[0].Effect
			effects := make([]Effect, len(sample.effects))
			wire.Prepared.Effects = make(preparedEffects, len(sample.effects))
			for index, item := range sample.effects {
				effects[index] = effect
				record := preparedEffect{
					ID:     wire.processID().effectID(wire.CommittedSteps+1, index),
					Effect: effect,
				}
				if item.phase != effectPhasePlanned {
					record.progress = &effectProgress{}
				}
				if item.settlement.Valid() {
					settlement, settlementErr := NewSettlement(item.settlement, json.RawMessage(`null`))
					if settlementErr != nil {
						t.Fatal(settlementErr)
					}
					record.progress.settlement = &settlement
				}
				wire.Prepared.Effects[index] = record
			}
			wire.Prepared.Intent, err = Continue(wire.Prepared.Intent.ConsumedSignals())
			if err != nil {
				t.Fatal(err)
			}
			data, err := jsonv2.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			_, err = parseTestProcessSnapshot(data)
			if sample.valid && err != nil {
				t.Fatalf("legal progress rejected: %v", err)
			}
			if !sample.valid && !errors.Is(err, ErrInvalidSnapshot) {
				t.Fatalf("illegal progress accepted: %v", err)
			}
		})
	}
}

func FuzzSnapshotJSONRoundTrip(f *testing.F) {
	snapshot := completedEngineTestSnapshot(f)
	f.Add([]byte(snapshot.JSON()))
	f.Fuzz(func(t *testing.T, data []byte) {
		parsed, err := parseTestProcessSnapshot(data)
		if err != nil {
			return
		}
		encoded, err := jsonv2.Marshal(parsed)
		if err != nil {
			t.Fatal(err)
		}
		reparsed, err := parseTestProcessSnapshot(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(parsed.JSON(), reparsed.JSON()) {
			t.Fatal("Snapshot changed across a strict JSON round trip")
		}
	})
}

func completedEngineTestSnapshot(t testing.TB) ProcessSnapshot {
	t.Helper()
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	deployment := engineTestDeployment(t, definition, &engineTestDispatcher{policy: ReplayPolicyNever})
	engine, err := NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter()})
	if err != nil {
		t.Fatal(err)
	}
	input, err := EncodePayload(engineTestInput{Value: "snapshot"})
	if err != nil {
		t.Fatal(err)
	}
	process, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	result, err := process.Await(context.Background())
	if err != nil || result.Status() != StatusCompleted {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	snapshot := inspectProcessSnapshot(t, process)
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func preparedEngineTestSnapshot(t testing.TB) ProcessSnapshot {
	t.Helper()
	committer := &recordingTreeCommitter{}
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	deployment := engineTestDeployment(t, definition, &engineTestDispatcher{policy: ReplayPolicyNever})
	engine, err := NewEngine(EngineConfig{Budget: Budget{Steps: NewQuota(10000), Effects: NewQuota(10000), Signals: NewQuota(100000)}, TreeCommitter: committer})
	if err != nil {
		t.Fatal(err)
	}
	input, err := EncodePayload(engineTestInput{Value: "prepared snapshot"})
	if err != nil {
		t.Fatal(err)
	}
	if _, runErr := engine.Run(context.Background(), deployment, input); runErr != nil {
		t.Fatal(runErr)
	}
	boundaries := committer.effectBoundaries()
	if len(boundaries) == 0 || boundaries[0].Kind() != EffectBoundaryKindPending {
		t.Fatalf("pending Effect boundary is missing: %#v", boundaries)
	}
	snapshot := boundaries[0].TreeSnapshot().ProcessSnapshots()[0]
	wire, err := snapshot.wire()
	if err != nil {
		t.Fatal(err)
	}
	wire.Prepared.Effects[0].progress = nil
	snapshot, err = newProcessSnapshot(wire)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestSnapshotAndChildResultPreserveNullOutput(t *testing.T) {
	wire, err := completedEngineTestSnapshot(t).wire()
	if err != nil {
		t.Fatal(err)
	}
	wire.Finish.Output, err = ParsePayload([]byte(`null`))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := newProcessSnapshot(wire)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := parseTestProcessSnapshot(snapshot.JSON())
	if err != nil {
		t.Fatal(err)
	}
	result, present := restored.Result()
	output, hasOutput := result.Output()
	if !present || !hasOutput || string(output.JSON()) != `null` {
		t.Fatalf("lost null output: %s", output.JSON())
	}
	for _, drained := range []bool{false, true} {
		original := ChildOutcome{result: result}
		if drained {
			original.descendantUnresolvedEffects = new([]UnresolvedEffect{})
		}
		data, err := jsonv2.Marshal(original)
		if err != nil {
			t.Fatal(err)
		}
		var decoded ChildOutcome
		if err := jsonv2.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		output, present := decoded.Result().Output()
		if !present || string(output.JSON()) != `null` {
			t.Fatalf("child output = %s", output.JSON())
		}
	}
	wire.Finish.Output = Payload{}
	if _, err := newProcessSnapshot(wire); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("missing output = %v", err)
	}
}

func TestSnapshotRejectsMissingAlwaysEmittedMembers(t *testing.T) {
	for _, snapshot := range []ProcessSnapshot{completedEngineTestSnapshot(t), preparedEngineTestSnapshot(t)} {
		for _, name := range []string{"committed_steps", "capabilities", "dropped_deltas", "mailbox", "pending_control"} {
			var fields map[string]json.RawMessage
			if err := jsonv2.Unmarshal(snapshot.JSON(), &fields); err != nil {
				t.Fatal(err)
			}
			delete(fields, name)
			if _, err := parseTestProcessSnapshot(controlValue(jsonv2.Marshal(fields))); !errors.Is(err, ErrInvalidSnapshot) {
				t.Fatalf("missing %s accepted: %v", name, err)
			}
		}
	}
}

func TestPreparedEffectIdentityFollowsBatchPosition(t *testing.T) {
	snapshot := preparedEngineTestSnapshot(t)
	wire := controlValue(snapshot.wire())
	for index, record := range wire.Prepared.Effects {
		if record.ID != wire.processID().effectID(wire.CommittedSteps+1, index) {
			t.Fatalf("decoded Effect %d identity = %s", index, record.ID)
		}
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
	effects[0]["id"] = controlValue(jsonv2.Marshal(wire.Prepared.Effects[0].ID))
	prepared["effects"] = controlValue(jsonv2.Marshal(effects))
	fields["prepared"] = controlValue(jsonv2.Marshal(prepared))
	if _, err := parseTestProcessSnapshot(controlValue(jsonv2.Marshal(fields))); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("snapshot accepted a stored copy of a derived EffectID: %v", err)
	}
}

// parseTestProcessSnapshot decodes a root Process record, the only record
// whose relation needs no enclosing tree.
func parseTestProcessSnapshot(data json.RawMessage) (ProcessSnapshot, error) {
	document, err := decodeProcessSnapshotDocument(data)
	if err != nil {
		return ProcessSnapshot{}, err
	}
	if _, child, linkErr := document.link(); linkErr != nil || child {
		return ProcessSnapshot{}, errors.Join(ErrInvalidSnapshot, linkErr, errors.New("test parses only root records"))
	}
	return document.snapshot(rootProcessRelation(document.ProcessID),
		func(ProcessID, ChildWaitBoundary) (ChildOutcome, error) {
			return ChildOutcome{}, errors.New("test root records have no children")
		},
		func(ProcessID) (ChildSpec, error) {
			return ChildSpec{}, errors.New("test root records have no children")
		})
}

func TestSnapshotFinishRequiresItsTerminationAndTime(t *testing.T) {
	snapshot := completedEngineTestSnapshot(t)
	var fields map[string]json.RawMessage
	if err := jsonv2.Unmarshal(snapshot.JSON(), &fields); err != nil {
		t.Fatal(err)
	}
	for _, missing := range []string{"termination", "finished_at"} {
		var finish map[string]json.RawMessage
		if err := jsonv2.Unmarshal(fields["finish"], &finish); err != nil {
			t.Fatal(err)
		}
		delete(finish, missing)
		candidate := maps.Clone(fields)
		candidate["finish"] = controlValue(jsonv2.Marshal(finish))
		if _, err := parseTestProcessSnapshot(controlValue(jsonv2.Marshal(candidate))); !errors.Is(err, ErrInvalidSnapshot) {
			t.Fatalf("finish without %s accepted: %v", missing, err)
		}
	}
}
