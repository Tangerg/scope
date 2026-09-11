package childcall

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent"
)

// Phase is the next Framework response accepted by a Single invocation.
type Phase uint8

const (
	AwaitingStart Phase = iota
	AwaitingOpening
	AwaitingCompletion
)

// Single owns the identities learned during one child invocation. Its zero
// value awaits the StartChild settlement; absence of an invocation belongs to
// the containing Strategy. Progress follows the identities already learned,
// so there is no separately persisted phase to contradict them.
type Single struct {
	processID agent.ProcessID
	waitID    agent.WaitID
}

func (s Single) Phase() Phase {
	if s.waitID.Valid() {
		return AwaitingCompletion
	}
	if s.processID.Valid() {
		return AwaitingOpening
	}
	return AwaitingStart
}

// ProcessID is available after a successful start, including to derive a
// Strategy-owned wait key from the actual invocation identity.
func (s Single) ProcessID() agent.ProcessID { return s.processID }

// AcceptStart records only a successfully started, exactly correlated child.
// A definite start failure leaves progress unchanged for the Strategy to handle.
func (s *Single) AcceptStart(signal agent.Signal, key agent.ChildKey, deployment agent.DeploymentRef) (agent.ChildStartResult, error) {
	if s.Phase() != AwaitingStart {
		return agent.ChildStartResult{}, errors.New("childcall: start already settled")
	}
	result, err := agent.ParseChildStartResult(signal)
	if err != nil {
		return agent.ChildStartResult{}, err
	}
	if !StartMatches(result, key, deployment) {
		return agent.ChildStartResult{}, errors.New("childcall: start does not match the declared child")
	}
	if processID, started := result.ProcessID(); started {
		s.processID = processID
	}
	return result, nil
}

func (s Single) waitSpec(key agent.WaitKey, boundary agent.ChildWaitBoundary) agent.ChildWaitSpec {
	return agent.ChildWaitSpec{
		Key: key, Boundary: boundary, Children: []agent.ProcessID{s.processID}, Condition: agent.AllChildren(),
	}
}

// WaitEffect requests the single child's declared completion boundary.
func (s Single) WaitEffect(key agent.WaitKey, boundary agent.ChildWaitBoundary) (agent.Effect, error) {
	if s.Phase() != AwaitingOpening {
		return agent.Effect{}, errors.New("childcall: wait requires a started child without an open wait")
	}
	return agent.WaitForChildren(s.waitSpec(key, boundary))
}

// AcceptOpening binds the Engine-assigned wait to the entire requested spec.
func (s *Single) AcceptOpening(signal agent.Signal, key agent.WaitKey, boundary agent.ChildWaitBoundary) (agent.WaitID, error) {
	if s.Phase() != AwaitingOpening {
		return agent.WaitID{}, errors.New("childcall: opening requires a started child without an open wait")
	}
	opened, err := agent.ParseChildWaitOpened(signal)
	if err != nil {
		return agent.WaitID{}, err
	}
	if !OpeningMatches(opened, s.waitSpec(key, boundary)) {
		return agent.WaitID{}, errors.New("childcall: opening does not match the declared wait")
	}
	s.waitID = opened.WaitID()
	return s.waitID, nil
}

// Complete extracts one exactly correlated result without interpreting its
// termination. The Strategy retires the invocation after applying its policy.
func (s Single) Complete(signal agent.Signal, key agent.ChildKey, waitKey agent.WaitKey, boundary agent.ChildWaitBoundary) (agent.Result, error) {
	if s.Phase() != AwaitingCompletion {
		return agent.Result{}, errors.New("childcall: completion requires an open wait")
	}
	completed, err := agent.ParseChildWaitSatisfied(signal)
	if err != nil {
		return agent.Result{}, err
	}
	if !CompletionMatches(completed, s.waitID, waitKey, boundary) {
		return agent.Result{}, errors.New("childcall: completion does not match the active wait")
	}
	outcomes := completed.Outcomes()
	if len(outcomes) != 1 || !OutcomeMatches(outcomes[0], key, s.processID) {
		return agent.Result{}, errors.New("childcall: completion does not identify the single child")
	}
	return outcomes[0].Result(), nil
}

type singleWire struct {
	ProcessID *agent.ProcessID `json:"process_id,omitempty"`
	WaitID    *agent.WaitID    `json:"wait_id,omitempty"`
}

func (s Single) MarshalJSON() ([]byte, error) {
	var wire singleWire
	if s.processID.Valid() {
		wire.ProcessID = &s.processID
	}
	if s.waitID.Valid() {
		wire.WaitID = &s.waitID
	}
	return json.Marshal(wire)
}

func (s *Single) UnmarshalJSON(data []byte) error {
	if s == nil {
		return errors.New("childcall: nil receiver")
	}
	var wire *singleWire
	if err := jsonv2.Unmarshal(data, &wire, jsonv2.RejectUnknownMembers(true)); err != nil {
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
