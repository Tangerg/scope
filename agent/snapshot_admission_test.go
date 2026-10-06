package agent

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

// The oracle deliberately materializes worst-case strings through the wire codec.
// It must remain independent of the arithmetic used by production admission.
func materializedAdmissionSize(p processSnapshotWire, limits TreeLimits) (uint64, error) {
	var pendingSize int
	if !p.terminal() && (limits.MaxProcessSnapshotBytes.limited || limits.MaxSnapshotBytes.limited) {
		failure := Failure{kind: FailureKindExecution, code: strings.Repeat("x", maxQualifiedNameBytes), message: strings.Repeat("\x00", MaxDiagnosticBytes)}
		if p.Prepared != nil {
			prepared := p.Prepared.clone()
			p.Prepared = &prepared
			for index := range prepared.Effects {
				record := &prepared.Effects[index]
				if err := materializeSnapshotSettlement(record, failure); err != nil {
					return 0, err
				}
			}
		}
		// JSON encodes NUL as six bytes (\u0000), the maximum expansion per UTF-8
		// byte. Current and pending control fields reserve independently, including
		// a Step pause racing a Host pause.
		reason := strings.Repeat("\x00", maxTerminationReasonBytes)
		pauseReason := strings.Repeat("\x00", maxPauseReasonBytes)
		p.PauseReason = pauseReason
		p.Counters.DroppedDeltas = ^uint64(0)
		p.PendingControl = pendingControlWire{
			Failure: &failure, KillReason: reason, PauseReason: pauseReason,
			Deadline:     &deadlineIntentWire{Owner: deadlineOwnerParent, Reason: reason},
			Cancellation: &cancellationIntentWire{Owner: cancellationOwnerParent, Reason: reason},
		}
		pending, err := jsonv2.Marshal(p)
		if err != nil {
			return 0, err
		}
		pendingSize = len(pending)
		p.PendingControl = pendingControlWire{}
		p.PauseReason = ""
		p.CurrentWaitID = nil
		// The maximal failure object is larger than a control cause and reason.
		p.Finish = &processFinish{
			Termination: failure.termination(),
			FinishedAt:  time.Date(9999, time.December, 31, 23, 59, 59, 999999999, time.UTC),
		}
	}
	encoded, err := jsonv2.Marshal(p)
	if err != nil {
		return 0, err
	}
	size := uint64(max(pendingSize, len(encoded)))
	if !limits.MaxProcessSnapshotBytes.Allows(size) {
		return 0, ErrResourceLimitExceeded
	}
	return size, nil
}

func materializeSnapshotSettlement(p *preparedEffect, failure Failure) error {
	if p.settlement() != nil {
		return nil
	}
	if p.Effect.Target() == EffectTargetDispatcher {
		if p.phase() == effectPhasePending {
			if err := p.settleUnknown(); err != nil {
				return err
			}
			p.progress.diagnostic = &failure
		}
		return nil
	}
	operation, operationErr := decodeFrameworkOperation(p.Effect.Payload())
	if operationErr != nil {
		return operationErr
	}
	switch operation.(type) {
	case waitOperation, childWaitOperation:
		return p.settleLocally(operation)
	case childStartOperation, childControlOperation:
		if p.phase() != effectPhasePending {
			return nil
		}
		return p.settleOperation(operation, failure)
	default:
		return ErrInvalidEffect
	}
}

func TestArithmeticAdmissionMatchesMaterializedWire(t *testing.T) {
	runtime := newWaitingSnapshotTree(t, 2)
	root := runtime.members.get(runtime.rootID)
	payload := json.RawMessage(`{"text":"<>&\n\"\\\u2028世界"}`)
	key := controlValue(ParseWaitKey("capacity"))
	child := newProcessID()
	request := controlValue(NewSignalRequest(controlValue(ParseSignalID("signal:capacity")), WaitID{}, payload))
	effects := []Effect{
		controlValue(NewDispatcherEffect(payload)),
		controlValue(NewWaitEffect(key)),
		controlValue(NewChildWaitEffect(ChildWaitSpec{Key: key, Boundary: ChildWaitBoundaryResult, Children: []ProcessID{child}, Condition: AllChildren()})),
		controlValue(NewChildStartEffect(ChildSpec{Key: controlValue(ParseChildKey("capacity")), DeploymentRef: root.deployment().DeploymentRef(), Input: controlValue(EncodePayload(childTestInput{Mode: "leaf"})), Budget: Budget{Steps: NewQuota(1)}})),
		controlValue(NewChildSignalEffect(child, request)),
		controlValue(NewChildCancelEffect(child, "stop")),
	}
	for _, treeQuota := range []bool{false, true} {
		for effectIndex, effect := range effects {
			for _, phase := range []effectPhase{effectPhasePlanned, effectPhasePending, effectPhaseSettled} {
				t.Run(fmt.Sprintf("tree_%t/effect_%d/%s", treeQuota, effectIndex, phase), func(t *testing.T) {
					wire := root.snapshotWire()
					limits := runtime.treeLimits
					if treeQuota {
						limits.MaxSnapshotBytes = NewQuota(1 << 30)
					} else {
						limits.MaxProcessSnapshotBytes = NewQuota(1 << 30)
					}
					record := preparedEffect{ID: root.handle.processID().effectID(1, 0), Effect: effect}
					if phase != effectPhasePlanned {
						record.progress = &effectProgress{}
					}
					if phase == effectPhaseSettled {
						if effect.Target() == EffectTargetDispatcher {
							settlement := controlValue(NewSettlement(SettlementStatusUnknown, payload))
							record.progress.settlement = &settlement
						} else if err := settleTestFramework(&record, Failure{}); err != nil {
							t.Fatal(err)
						}
						diagnostic := controlValue(NewFailure(FailureKindExternal, "test.failure", "<actual diagnostic>"))
						record.progress.diagnostic = &diagnostic
					}
					wire.Prepared = &preparedStep{CandidateState: root.committedExecutionState, Intent: controlValue(Continue(0)), Effects: preparedEffects{record}}
					before := controlValue(jsonv2.Marshal(wire))
					want := controlValue(materializedAdmissionSize(wire, limits))
					got := controlValue(wire.admissionSize(limits))
					if got != want {
						t.Fatalf("size = %d, materialized = %d", got, want)
					}
					if after := controlValue(jsonv2.Marshal(wire)); !bytes.Equal(before, after) {
						t.Fatal("admission mutated source wire")
					}
					limits.MaxProcessSnapshotBytes = NewQuota(math.MaxUint64)
					exact := controlValue(materializedAdmissionSize(wire, limits))
					limits.MaxProcessSnapshotBytes = NewQuota(exact)
					if _, err := wire.admissionSize(limits); err != nil {
						t.Fatalf("exact quota: %v", err)
					}
					limits.MaxProcessSnapshotBytes = NewQuota(exact - 1)
					if _, err := wire.admissionSize(limits); !errors.Is(err, ErrResourceLimitExceeded) {
						t.Fatalf("one byte short: %v", err)
					}
				})
			}
		}
	}
	limits := runtime.treeLimits
	limits.MaxSnapshotBytes = NewQuota(1 << 30)
	for _, process := range runtime.members.all() {
		wire := process.snapshotWire()
		for _, reason := range []string{"", "<paused>"} {
			wire.PauseReason = reason
			if got, want := controlValue(wire.admissionSize(limits)), controlValue(materializedAdmissionSize(wire, limits)); got != want {
				t.Fatalf("pause %q: %d != %d", wire.PauseReason, got, want)
			}
		}
		wire.PauseReason, wire.CurrentWaitID = "", nil
		wire.Finish = &processFinish{
			Termination: controlValue(NewFailure(FailureKindExecution, "test.failure", "done")).termination(),
			FinishedAt:  time.Now().UTC(),
		}
		if got, want := controlValue(wire.admissionSize(limits)), uint64(len(controlValue(jsonv2.Marshal(wire)))); got != want {
			t.Fatalf("terminal: %d != %d", got, want)
		}
	}
}

func TestArithmeticAdmissionReservesLargeUncertainBatch(t *testing.T) {
	runtime := newWaitingSnapshotTree(t, 1)
	root := runtime.members.get(runtime.rootID)
	wire := root.snapshotWire()
	limits := runtime.treeLimits
	limits.MaxSnapshotBytes = NewQuota(1 << 30)
	wire.Prepared = &preparedStep{CandidateState: root.committedExecutionState, Intent: controlValue(Continue(0))}
	effect := controlValue(NewDispatcherEffect(json.RawMessage(`{}`)))
	for index := range 3000 {
		id := root.handle.processID().effectID(1, index)
		settlement := controlValue(NewSettlement(SettlementStatusUnknown, json.RawMessage(`null`)))
		wire.Prepared.Effects = append(wire.Prepared.Effects, preparedEffect{ID: id, Effect: effect, progress: &effectProgress{settlement: &settlement}})
	}
	if got, want := controlValue(wire.admissionSize(limits)), controlValue(materializedAdmissionSize(wire, limits)); got != want {
		t.Fatalf("uncertain batch: %d != %d", got, want)
	}
}

func TestSnapshotReservationAllocation(t *testing.T) {
	runtime := newWaitingSnapshotTree(t, 10)
	measure := func() int64 {
		result := testing.Benchmark(func(b *testing.B) {
			for b.Loop() {
				for _, process := range runtime.members.all() {
					if _, err := process.snapshotWire().admissionSize(runtime.treeLimits); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
		return result.AllocedBytesPerOp()
	}
	// Compare encoding with and without reservation, independently of the
	// unlimited admission fast path, which intentionally performs no encoding.
	unlimited := measure()
	runtime.treeLimits.MaxProcessSnapshotBytes = NewQuota(1 << 20)
	limited := measure()
	// Reservation may encode two lifecycle envelopes, but must not allocate
	// the worst-case diagnostic contents represented by their byte counts.
	if limited > 4*unlimited {
		t.Fatalf("finite reservation allocated %d bytes; unlimited used %d", limited, unlimited)
	}
}
