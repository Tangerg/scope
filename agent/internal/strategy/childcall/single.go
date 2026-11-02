// Package childcall owns recoverable child handshake progress. Strategies own
// invocation keys, signal consumption, result interpretation, and failure policy.
package childcall

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
	"github.com/Tangerg/scope/agent/run"
)

// Phase identifies the next Framework response accepted by child invocation progress.
type Phase uint8

const (
	PhaseAwaitingStart Phase = iota
	PhaseAwaitingOpening
	PhaseAwaitingCompletion
)

// Single owns the identities learned during one child invocation. Its zero
// value awaits the NewChildStartEffect settlement; absence of an invocation belongs to
// the containing Strategy. Progress follows the identities already learned,
// so there is no separately persisted phase to contradict them.
type Single struct {
	processID run.ProcessID
	waitID    run.WaitID
}

// Window validates the input cardinality owned by this handshake. An opening
// may be followed by its completion when the child has already drained.
func (s Single) Window(signals []run.Signal) (run.Signal, error) {
	if len(signals) == 0 || len(signals) > 1 && s.Phase() != PhaseAwaitingOpening || len(signals) > 2 {
		return run.Signal{}, errors.New("childcall: unexpected handshake window size")
	}
	if len(signals) == 2 {
		opened, err := signals[0].Frame[run.ChildWaitOpened]()
		if err != nil {
			return run.Signal{}, err
		}
		completed, err := signals[1].Frame[run.ChildWaitSatisfied]()
		if err != nil || completed.WaitID() != opened.WaitID() {
			return run.Signal{}, errors.New("childcall: opening suffix is not its completion")
		}
	}
	return signals[0], nil
}

func (s Single) Phase() Phase {
	if s.waitID.Valid() {
		return PhaseAwaitingCompletion
	}
	if s.processID.Valid() {
		return PhaseAwaitingOpening
	}
	return PhaseAwaitingStart
}

// ProcessID is available after a successful start, including to derive a
// Strategy-owned wait key from the actual invocation identity.
func (s Single) ProcessID() run.ProcessID { return s.processID }

// AcceptStart records only a successfully started child. A definite start
// failure leaves progress unchanged for the Strategy to handle.
func (s *Single) AcceptStart(signal run.Signal) (run.ChildStartResult, error) {
	if s.Phase() != PhaseAwaitingStart {
		return run.ChildStartResult{}, errors.New("childcall: start already settled")
	}
	result, err := signal.Frame[run.ChildStartResult]()
	if err != nil {
		return run.ChildStartResult{}, err
	}
	if processID, started := result.ProcessID(); started {
		s.processID = processID
	}
	return result, nil
}

func (s Single) waitSpec(key run.WaitKey, boundary run.ChildWaitBoundary) run.ChildWaitSpec {
	return run.ChildWaitSpec{
		Key: key, Boundary: boundary, Children: []run.ProcessID{s.processID}, Condition: run.AllChildren(),
	}
}

func (s Single) WaitEffect(key run.WaitKey, boundary run.ChildWaitBoundary) (run.Effect, error) {
	if s.Phase() != PhaseAwaitingOpening {
		return run.Effect{}, errors.New("childcall: wait requires a started child without an open wait")
	}
	return run.NewChildWaitEffect(s.waitSpec(key, boundary))
}

func (s *Single) AcceptOpening(signal run.Signal) (run.WaitID, error) {
	if s.Phase() != PhaseAwaitingOpening {
		return run.WaitID{}, errors.New("childcall: opening requires a started child without an open wait")
	}
	opened, err := signal.Frame[run.ChildWaitOpened]()
	if err != nil {
		return run.WaitID{}, err
	}
	s.waitID = opened.WaitID()
	return s.waitID, nil
}

// Complete extracts the single child's outcome without interpreting its
// termination or subtree facts. The Strategy retires the invocation after
// applying its policy.
func (s Single) Complete(signal run.Signal, waitKey run.WaitKey, boundary run.ChildWaitBoundary) (run.ChildOutcome, error) {
	if s.Phase() != PhaseAwaitingCompletion {
		return run.ChildOutcome{}, errors.New("childcall: completion requires an open wait")
	}
	completed, err := signal.Frame[run.ChildWaitSatisfied]()
	if err != nil {
		return run.ChildOutcome{}, err
	}
	if !completed.Matches(s.waitID, s.waitSpec(waitKey, boundary)) {
		return run.ChildOutcome{}, errors.New("childcall: completion does not match the active wait")
	}
	// Matching the single-child wait admits exactly its one outcome.
	return completed.Outcomes()[0], nil
}

type singleWire struct {
	ProcessID *run.ProcessID `json:"process_id,omitzero"`
	WaitID    *run.WaitID    `json:"wait_id,omitzero"`
}

func (s Single) MarshalJSON() ([]byte, error) {
	var wire singleWire
	if s.processID.Valid() {
		wire.ProcessID = &s.processID
	}
	if s.waitID.Valid() {
		wire.WaitID = &s.waitID
	}
	return jsonv2.Marshal(wire)
}

func (s *Single) UnmarshalJSON(data []byte) error {
	if s == nil {
		return errors.New("childcall: nil receiver")
	}
	wire, err := jsonwire.Decode[*singleWire](data)
	if err != nil {
		return fmt.Errorf("childcall: decode progress: %w", err)
	}
	if wire == nil {
		return errors.New("childcall: progress must be an object")
	}
	if wire.WaitID != nil && wire.ProcessID == nil {
		return errors.New("childcall: wait has no child")
	}
	var value Single
	if wire.ProcessID != nil {
		value.processID = *wire.ProcessID
	}
	if wire.WaitID != nil {
		value.waitID = *wire.WaitID
	}
	*s = value
	return nil
}
