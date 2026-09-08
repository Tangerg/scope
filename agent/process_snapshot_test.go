package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func TestSnapshotStrictlyRejectsUnknownFields(t *testing.T) {
	snapshot := completedEngineTestSnapshot(t)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(snapshot.JSON(), &fields); err != nil {
		t.Fatal(err)
	}
	fields["application_revision"] = json.RawMessage(`1`)
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseProcessSnapshot(data); err == nil {
		t.Fatal("ParseSnapshot accepted an unknown application field")
	}
}

func TestPreparedSnapshotBindsCommittedExecutionState(t *testing.T) {
	snapshot := preparedEngineTestSnapshot(t)
	var projection struct {
		CommittedState ExecutionState `json:"committed_execution_state"`
		Prepared       struct {
			CommittedDigest Digest         `json:"committed_execution_state_digest"`
			CandidateState  ExecutionState `json:"candidate_state"`
		} `json:"prepared"`
	}
	if err := json.Unmarshal(snapshot.JSON(), &projection); err != nil {
		t.Fatal(err)
	}
	committed, err := json.Marshal(snapshot.CommittedExecutionState())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(projection.CommittedState)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := json.Marshal(projection.Prepared.CandidateState)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(committed, encoded) || bytes.Equal(committed, candidate) ||
		projection.Prepared.CommittedDigest != ComputeDigest(committed) {
		t.Fatal("prepared snapshot did not preserve and identify the committed state")
	}
	wire, err := snapshot.wire()
	if err != nil {
		t.Fatal(err)
	}
	wire.Prepared.CommittedExecutionStateDigest = ComputeDigest(candidate)
	if _, err := newProcessSnapshot(wire); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("prepared Step bound to its own candidate: %v", err)
	}
}

func TestSnapshotRejectsAcceptedSignalCountThatDisagreesWithMailbox(t *testing.T) {
	snapshot := completedEngineTestSnapshot(t)
	wire, err := snapshot.wire()
	if err != nil {
		t.Fatal(err)
	}
	if wire.Usage.AcceptedSignals == 0 {
		t.Fatal("test fixture contains no accepted Signal")
	}
	wire.Usage.AcceptedSignals--
	data, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseProcessSnapshot(data); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("accepted Signal mismatch error = %v, want ErrInvalidSnapshot", err)
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
	wire.Usage.CommittedSteps = math.MaxUint64
	wire.Limits.MaxSteps = math.MaxUint64
	wire.Budget.Steps = math.MaxUint64
	wire.Prepared.StepSequence = 0
	wire.Prepared.Effects[0].ID = deriveEffectID(wire.ProcessID, 0, 0)
	data, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseProcessSnapshot(data); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("prepared Step overflow error = %v, want ErrInvalidSnapshot", err)
	}
}

func TestSnapshotAccountsForPreparedEffectIdentities(t *testing.T) {
	snapshot := preparedEngineTestSnapshot(t)
	wire, err := snapshot.wire()
	if err != nil {
		t.Fatal(err)
	}
	for _, count := range []uint64{0, 1, 2} {
		wire.Usage.PreparedEffects = count
		data, encodeErr := json.Marshal(wire)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		_, parseErr := ParseProcessSnapshot(data)
		if count == 0 {
			if !errors.Is(parseErr, ErrInvalidSnapshot) {
				t.Errorf("uncounted prepared Effect parse error = %v; want ErrInvalidSnapshot", parseErr)
			}
		} else if parseErr != nil {
			t.Errorf("counted prepared Effect with usage %d rejected: %v", count, parseErr)
		}
	}
}

func TestPreparedEffectPhaseOwnsMonotonicTransitions(t *testing.T) {
	snapshot := preparedEngineTestSnapshot(t)
	wire, err := snapshot.wire()
	if err != nil {
		t.Fatal(err)
	}
	record := &wire.Prepared.Effects[0]
	if record.Phase != effectPhasePlanned {
		t.Fatalf("initial phase = %s, want %s", record.Phase, effectPhasePlanned)
	}
	if beginErr := record.begin(); beginErr != nil || record.Phase != effectPhasePending {
		t.Fatalf("begin phase = %s, error = %v", record.Phase, beginErr)
	}
	settlement, err := NewSettlement(record.ID, SettlementStatusUnknown, json.RawMessage(`null`))
	if err != nil {
		t.Fatal(err)
	}
	if settleErr := record.settle(settlement); settleErr != nil || !record.unknown() {
		t.Fatalf("settle phase = %s, unknown = %t, error = %v", record.Phase, record.unknown(), settleErr)
	}
	definite, err := NewSettlement(record.ID, SettlementStatusSucceeded, json.RawMessage(`{"ok":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := record.resolveUnknown(definite); err != nil || !record.definitelySettled() {
		t.Fatalf("resolve phase = %s, definite = %t, error = %v", record.Phase, record.definitelySettled(), err)
	}
	if err := record.begin(); err == nil {
		t.Fatal("settled Effect moved backward to pending")
	}
}

func TestPreparedEffectPhaseAndSettlementMustAgree(t *testing.T) {
	snapshot := preparedEngineTestSnapshot(t)
	wire, err := snapshot.wire()
	if err != nil {
		t.Fatal(err)
	}
	record := &wire.Prepared.Effects[0]
	record.Phase = effectPhaseSettled
	data, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseProcessSnapshot(data); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("phase/settlement mismatch error = %v, want ErrInvalidSnapshot", err)
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
				record := preparedEffectWire{
					ID:     deriveEffectID(wire.ProcessID, wire.Prepared.StepSequence, index),
					Effect: effect, Phase: item.phase,
				}
				if item.settlement.Valid() {
					settlement, settlementErr := NewSettlement(record.ID, item.settlement, json.RawMessage(`null`))
					if settlementErr != nil {
						t.Fatal(settlementErr)
					}
					record.Settlement = &settlement
				}
				wire.Prepared.Effects[index] = record
			}
			wire.Prepared.Transition, err = Continue(wire.Prepared.Transition.ConsumedSignals(), effects...)
			if err != nil {
				t.Fatal(err)
			}
			wire.Usage.PreparedEffects = uint64(len(effects))
			data, err := json.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			_, err = ParseProcessSnapshot(data)
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
		parsed, err := ParseProcessSnapshot(data)
		if err != nil {
			return
		}
		encoded, err := json.Marshal(parsed)
		if err != nil {
			t.Fatal(err)
		}
		reparsed, err := ParseProcessSnapshot(encoded)
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
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	input, err := EncodeInput(engineTestInput{Value: "snapshot"})
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
	snapshot, err := process.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func preparedEngineTestSnapshot(t testing.TB) ProcessSnapshot {
	t.Helper()
	durability := &recordingTreeDurability{}
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	deployment := engineTestDeployment(t, definition, &engineTestDispatcher{policy: ReplayPolicyNever})
	engine, err := NewEngine(EngineConfig{TreeDurability: durability})
	if err != nil {
		t.Fatal(err)
	}
	input, err := EncodeInput(engineTestInput{Value: "prepared snapshot"})
	if err != nil {
		t.Fatal(err)
	}
	if _, runErr := engine.Run(context.Background(), deployment, input); runErr != nil {
		t.Fatal(runErr)
	}
	boundaries := durability.effectBoundaries()
	if len(boundaries) == 0 || boundaries[0].Kind() != EffectBoundaryPending {
		t.Fatalf("pending Effect boundary is missing: %#v", boundaries)
	}
	snapshot := boundaries[0].TreeSnapshot().ProcessSnapshots()[0]
	wire, err := snapshot.wire()
	if err != nil {
		t.Fatal(err)
	}
	wire.Prepared.Effects[0].Phase = effectPhasePlanned
	snapshot, err = newProcessSnapshot(wire)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	return snapshot
}
