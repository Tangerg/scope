package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
)

func (t *treeSnapshotValidation) validateChildWaitSignals(mailbox mailboxWire, waitID WaitID, spec ChildWaitSpec) error {
	opened, err := encodeChildWaitOpened(spec)
	if err != nil {
		return fmt.Errorf("%w: encode child wait: %w", ErrInvalidTreeSnapshot, err)
	}
	opened, err = wireJSON.normalize(opened, maxWireBytes)
	if err != nil {
		return fmt.Errorf("%w: normalize child wait: %w", ErrInvalidTreeSnapshot, err)
	}
	for _, record := range mailbox.Signals {
		if record.WaitID == nil || *record.WaitID != waitID {
			continue
		}
		if record.OpensWait {
			if record.PayloadDigest != ComputeDigest(opened) {
				return fmt.Errorf("%w: child wait disagrees with its opening Signal", ErrInvalidTreeSnapshot)
			}
			continue
		}
		signal, signalErr := newSignal(record.ID, waitID, record.Payload)
		if signalErr != nil {
			return fmt.Errorf("%w: invalid child wait Signal: %w", ErrInvalidTreeSnapshot, signalErr)
		}
		satisfied, parseErr := ParseChildWaitSatisfied(signal)
		if parseErr != nil || record.ID != deriveChildWaitSignalID(waitID) ||
			satisfied.Key() != spec.Key || satisfied.Boundary() != spec.Boundary {
			return fmt.Errorf("%w: child wait satisfaction disagrees with its registration", ErrInvalidTreeSnapshot)
		}
		required, _ := spec.Condition.required(len(spec.Children))
		if uint32(len(satisfied.outcomes)) < required {
			return fmt.Errorf("%w: child wait condition is unsatisfied", ErrInvalidTreeSnapshot)
		}
		previous := -1
		for _, outcome := range satisfied.outcomes {
			index := slices.Index(spec.Children, outcome.result.ProcessID())
			if index <= previous || !t.matchesChildWaitOutcome(outcome, spec.Boundary) {
				return fmt.Errorf("%w: child wait outcome disagrees with its tree", ErrInvalidTreeSnapshot)
			}
			previous = index
		}
	}
	return nil
}

func (t *treeSnapshotValidation) matchesChildWaitOutcome(outcome ChildOutcome, boundary ChildWaitBoundary) bool {
	child, exists := t.processes[outcome.result.ProcessID()]
	if !exists || !child.Status.Terminal() || child.Relation.ChildKey == nil || *child.Relation.ChildKey != outcome.key {
		return false
	}
	expected := resultWire{
		ProcessID: child.ProcessID, StartedAt: child.StartedAt, FinishedAt: *child.FinishedAt,
		Output: child.Output, Termination: *child.Termination, Usage: child.Usage,
	}
	expectedJSON, expectedErr := json.Marshal(expected)
	actualJSON, actualErr := json.Marshal(resultWireFromValue(outcome.result))
	if expectedErr != nil || actualErr != nil || !bytes.Equal(expectedJSON, actualJSON) {
		return false
	}
	return boundary != ChildWaitBoundaryDrained || t.subtreeTerminal(child.ProcessID)
}

func (t *treeSnapshotValidation) subtreeTerminal(processID ProcessID) bool {
	if !t.processes[processID].Status.Terminal() {
		return false
	}
	for _, child := range t.processes {
		if child.Relation.ParentID != nil && *child.Relation.ParentID == processID && !t.subtreeTerminal(child.ProcessID) {
			return false
		}
	}
	return true
}
