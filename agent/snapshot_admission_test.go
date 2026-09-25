package agent

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The oracle deliberately materializes worst-case strings through the wire codec.
// It must remain independent of the arithmetic used by production admission.
func materializedAdmissionSize(p processSnapshotWire) (uint64, error) {
	var pendingSize int
	if !p.Status.Terminal() && (p.Limits.MaxSnapshotBytes.limited || p.TreeLimits.MaxSnapshotBytes.limited) {
		failure := Failure{kind: FailureKindExecution, code: strings.Repeat("x", maxFailureCodeBytes), message: strings.Repeat("\x00", MaxDiagnosticBytes)}
		var unresolved []EffectID
		if p.Prepared != nil {
			prepared := p.Prepared.clone()
			p.Prepared = &prepared
			for index := range prepared.Effects {
				record := &prepared.Effects[index]
				if record.unknown() || record.Phase == effectPhasePending {
					unresolved = append(unresolved, record.ID)
				}
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
		p.Status = StatusRunning
		p.Counters.DroppedDeltas = ^uint64(0)
		p.PendingControl = pendingControlWire{
			Failure: &failure, KillReason: reason, PauseReason: pauseReason,
			DeadlineOwner: deadlineOwnerParent, DeadlineReason: reason,
			CancellationOwner: cancellationOwnerParent, CancellationReason: reason,
		}
		pending, err := jsonv2.Marshal(p)
		if err != nil {
			return 0, err
		}
		pendingSize = len(pending)
		p.PendingControl = pendingControlWire{}
		p.PauseReason = ""
		p.CurrentWaitID = nil
		p.Status = StatusFailed
		p.FinishedAt = new(time.Date(9999, time.December, 31, 23, 59, 59, 999999999, time.UTC))
		// The maximal failure object adds more bytes than other terminal statuses
		// and causes can add, while its message also fills the termination reason.
		termination := failure.termination().withUnresolvedEffectIDs(unresolved)
		p.Termination = &termination
	}
	encoded, err := jsonv2.Marshal(p)
	if err != nil {
		return 0, err
	}
	size := uint64(max(pendingSize, len(encoded)))
	if !p.Limits.MaxSnapshotBytes.Allows(size) {
		return 0, ErrResourceLimitExceeded
	}
	return size, nil
}

func materializeSnapshotSettlement(p *preparedEffect, failure Failure) error {
	if p.Settlement != nil {
		return nil
	}
	if p.Effect.Target() == EffectTargetDispatcher {
		if p.Phase == effectPhasePending {
			if err := p.settleUnknown(); err != nil {
				return err
			}
			p.Diagnostic = &failure
		}
		return nil
	}
	operation, operationErr := decodeFrameworkEffectOperation(p.Effect.Payload())
	if operationErr != nil {
		return operationErr
	}
	if operation == frameworkEffectWait || operation == frameworkEffectWaitChildren {
		if p.Phase == effectPhasePlanned {
			if err := p.begin(); err != nil {
				return err
			}
		}
		return p.settleFramework()
	}
	if p.Phase != effectPhasePending {
		return nil
	}
	if operation == frameworkEffectStartChild {
		spec, err := decodeChildStartEffect(p.Effect.Payload())
		if err != nil {
			return err
		}
		return p.settleChildStart(ChildStartResult{key: spec.Key, deploymentRef: spec.DeploymentRef, failure: failure})
	}
	request, err := decodeChildControlEffect(p.Effect.Payload())
	if err != nil {
		return err
	}
	result := ChildControlResult{childID: request.ChildID, operation: request.Operation, failure: failure}
	if request.Signal != nil {
		result.signalID = request.Signal.ID()
	}
	return p.settleChildControl(result)
}

func TestArithmeticAdmissionMatchesMaterializedWire(t *testing.T) {
	runtime := newWaitingSnapshotTree(t, 2)
	root := runtime.processes[runtime.rootID]
	payload := json.RawMessage(`{"text":"<>&\n\"\\\u2028世界"}`)
	key := controlValue(ParseWaitKey("capacity"))
	child := newProcessID()
	request := controlValue(NewSignalRequest(controlValue(ParseSignalID("signal:capacity")), WaitID{}, payload))
	effects := []Effect{
		controlValue(NewDispatcherEffect(payload)),
		controlValue(NewWaitEffect(key, payload)),
		controlValue(NewChildWaitEffect(ChildWaitSpec{Key: key, Boundary: ChildWaitBoundaryResult, Children: []ProcessID{child}, Condition: AllChildren()})),
		controlValue(NewChildStartEffect(ChildSpec{Key: controlValue(ParseChildKey("capacity")), DeploymentRef: root.deployment.DeploymentRef(), Input: controlValue(EncodePayload(childTestInput{Mode: "leaf"})), Budget: Budget{Steps: NewQuota(1)}})),
		controlValue(NewChildSignalEffect(child, request)),
		controlValue(NewChildCancelEffect(child, "stop")),
	}
	for _, treeQuota := range []bool{false, true} {
		for effectIndex, effect := range effects {
			for _, phase := range []effectPhase{effectPhasePlanned, effectPhasePending, effectPhaseSettled} {
				t.Run(fmt.Sprintf("tree_%t/effect_%d/%s", treeQuota, effectIndex, phase), func(t *testing.T) {
					wire := root.snapshotWire()
					if treeQuota {
						wire.TreeLimits.MaxSnapshotBytes = NewQuota(1 << 30)
					} else {
						wire.Limits.MaxSnapshotBytes = NewQuota(1 << 30)
					}
					record := preparedEffect{ID: root.handle.processID.effectID(1, 0), Effect: effect, Phase: phase}
					if phase == effectPhaseSettled {
						status := SettlementStatusSucceeded
						if effect.Target() == EffectTargetDispatcher {
							status = SettlementStatusUnknown
						}
						settlement := controlValue(NewSettlement(record.ID, status, payload))
						record.Settlement = &settlement
						diagnostic := controlValue(NewFailure(FailureKindExternal, "test.failure", "<actual diagnostic>"))
						record.Diagnostic = &diagnostic
					}
					wire.Prepared = &preparedStep{StepSequence: 1, CommittedExecutionStateDigest: controlValue(root.committedExecutionState.digest()), CandidateState: root.committedExecutionState, Intent: controlValue(Continue(0)), Effects: preparedEffects{record}}
					before := controlValue(jsonv2.Marshal(wire))
					want := controlValue(materializedAdmissionSize(wire))
					got := controlValue(wire.admissionSize())
					if got != want {
						t.Fatalf("size = %d, materialized = %d", got, want)
					}
					if after := controlValue(jsonv2.Marshal(wire)); !bytes.Equal(before, after) {
						t.Fatal("admission mutated source wire")
					}
					// The quota's decimal width participates in its own encoded size.
					wire.Limits.MaxSnapshotBytes = NewQuota(999999)
					exact := controlValue(materializedAdmissionSize(wire))
					wire.Limits.MaxSnapshotBytes = NewQuota(exact)
					if _, err := wire.admissionSize(); err != nil {
						t.Fatalf("exact quota: %v", err)
					}
					wire.Limits.MaxSnapshotBytes = NewQuota(exact - 1)
					if _, err := wire.admissionSize(); !errors.Is(err, ErrResourceLimitExceeded) {
						t.Fatalf("one byte short: %v", err)
					}
				})
			}
		}
	}
	for _, process := range runtime.processes {
		wire := process.snapshotWire()
		wire.TreeLimits.MaxSnapshotBytes = NewQuota(1 << 30)
		for _, status := range []Status{StatusRunning, StatusWaiting, StatusPaused} {
			wire.Status = status
			wire.PauseReason = "<paused>"
			if got, want := controlValue(wire.admissionSize()), controlValue(materializedAdmissionSize(wire)); got != want {
				t.Fatalf("%s: %d != %d", status, got, want)
			}
		}
		wire.Status = StatusFailed
		termination := controlValue(NewFailure(FailureKindExecution, "test.failure", "done")).termination()
		wire.Termination = &termination
		wire.FinishedAt = new(time.Now().UTC())
		if got, want := controlValue(wire.admissionSize()), uint64(len(controlValue(jsonv2.Marshal(wire)))); got != want {
			t.Fatalf("terminal: %d != %d", got, want)
		}
	}
}

func TestArithmeticAdmissionReservesLargeUnresolvedTermination(t *testing.T) {
	runtime := newWaitingSnapshotTree(t, 1)
	root := runtime.processes[runtime.rootID]
	wire := root.snapshotWire()
	wire.TreeLimits.MaxSnapshotBytes = NewQuota(1 << 30)
	wire.Prepared = &preparedStep{StepSequence: 1, CommittedExecutionStateDigest: controlValue(root.committedExecutionState.digest()), CandidateState: root.committedExecutionState, Intent: controlValue(Continue(0))}
	effect := controlValue(NewDispatcherEffect(json.RawMessage(`{}`)))
	for index := range 3000 {
		id := root.handle.processID.effectID(1, index)
		settlement := controlValue(NewSettlement(id, SettlementStatusUnknown, json.RawMessage(`null`)))
		wire.Prepared.Effects = append(wire.Prepared.Effects, preparedEffect{ID: id, Effect: effect, Phase: effectPhaseSettled, Settlement: &settlement})
	}
	if got, want := controlValue(wire.admissionSize()), controlValue(materializedAdmissionSize(wire)); got != want {
		t.Fatalf("unresolved termination: %d != %d", got, want)
	}
}

func TestSnapshotReservationAllocation(t *testing.T) {
	runtime := newWaitingSnapshotTree(t, 10)
	measure := func() int64 {
		result := testing.Benchmark(func(b *testing.B) {
			for b.Loop() {
				for _, process := range runtime.processes {
					if _, err := process.snapshotWire().admissionSize(); err != nil {
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
	for _, process := range runtime.processes {
		process.limits.MaxSnapshotBytes = NewQuota(1 << 20)
	}
	limited := measure()
	// Reservation may encode two lifecycle envelopes, but must not allocate
	// the worst-case diagnostic contents represented by their byte counts.
	if limited > 4*unlimited {
		t.Fatalf("finite reservation allocated %d bytes; unlimited used %d", limited, unlimited)
	}
}
