package childcall

import (
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent"
)

// ErrInvalidBatch identifies invalid child identity or wait structure, independently of handshake phase.
var ErrInvalidBatch = errors.New("childcall: invalid batch")

// Child is a projection of one Strategy-owned invocation. Done means that the
// Strategy has handled its outcome or rejected it without a Process; completed
// entries still retain their ProcessID so a later admission cannot reuse an
// earlier child's identity.
type Child struct {
	Key       agent.ChildKey
	ProcessID agent.ProcessID
	Done      bool
}

// Batch owns ordered handshake validation over a Strategy's current invocation
// facts. It is a temporary view, never a second persisted state machine. Methods
// validate structure before phase, then the whole response before returning
// indices for the Strategy to adopt; they never mutate the supplied facts.
// Scheduling and result policy, including partial admissions and refilling a
// bounded window, stay with the Strategy.
type Batch struct {
	Children []Child
	WaitID   agent.WaitID
}

func (b Batch) Phase() Phase {
	if b.PendingStarts() > 0 {
		return PhaseAwaitingStart
	}
	if b.WaitID.Valid() {
		return PhaseAwaitingCompletion
	}
	return PhaseAwaitingOpening
}

func (b Batch) PendingStarts() int {
	pending := 0
	for _, child := range b.Children {
		if !child.Done && !child.ProcessID.Valid() {
			pending++
		}
	}
	return pending
}

func (b Batch) Validate() error {
	keys := make(map[agent.ChildKey]struct{}, len(b.Children))
	processes := make(map[agent.ProcessID]struct{}, len(b.Children))
	for _, child := range b.Children {
		if (!child.Done || child.ProcessID.Valid()) && !child.Key.Valid() {
			return fmt.Errorf("%w: active child has no key", ErrInvalidBatch)
		}
		if child.Key.Valid() {
			if _, duplicate := keys[child.Key]; duplicate {
				return fmt.Errorf("%w: duplicate child key", ErrInvalidBatch)
			}
			keys[child.Key] = struct{}{}
		}
		if child.ProcessID.Valid() {
			if _, duplicate := processes[child.ProcessID]; duplicate {
				return fmt.Errorf("%w: duplicate child Process", ErrInvalidBatch)
			}
			processes[child.ProcessID] = struct{}{}
		}
	}
	if b.WaitID.Valid() && b.Phase() == PhaseAwaitingStart {
		return fmt.Errorf("%w: open wait precedes child admission", ErrInvalidBatch)
	}
	return nil
}

// AcceptStarts validates an ordered prefix of pending admissions atomically;
// starts settle in declaration order, so the i-th start answers the i-th
// pending child. Callers decide whether their signal window must settle all
// pending admissions.
func (b Batch) AcceptStarts(starts []agent.ChildStartResult) ([]int, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	if b.Phase() != PhaseAwaitingStart || len(starts) == 0 {
		return nil, errors.New("childcall: no pending start response")
	}
	seen := make(map[agent.ProcessID]struct{}, len(b.Children))
	for _, child := range b.Children {
		if child.ProcessID.Valid() {
			seen[child.ProcessID] = struct{}{}
		}
	}
	var indices []int
	for index, child := range b.Children {
		if child.Done || child.ProcessID.Valid() {
			continue
		}
		if len(indices) == len(starts) {
			break
		}
		start := starts[len(indices)]
		if !start.Valid() {
			return nil, errors.New("childcall: invalid start response")
		}
		if id, started := start.ProcessID(); started {
			if _, duplicate := seen[id]; duplicate {
				return nil, errors.New("childcall: start reuses a child Process")
			}
			seen[id] = struct{}{}
		}
		indices = append(indices, index)
	}
	if len(indices) != len(starts) {
		return nil, errors.New("childcall: excess start responses")
	}
	return indices, nil
}

func (b Batch) WaitSpec(key agent.WaitKey, boundary agent.ChildWaitBoundary, condition agent.ChildWaitCondition) (agent.ChildWaitSpec, error) {
	if err := b.Validate(); err != nil {
		return agent.ChildWaitSpec{}, err
	}
	if b.Phase() == PhaseAwaitingStart {
		return agent.ChildWaitSpec{}, errors.New("childcall: wait precedes child admission")
	}
	spec := agent.ChildWaitSpec{Key: key, Boundary: boundary, Condition: condition}
	for _, child := range b.Children {
		if !child.Done && child.ProcessID.Valid() {
			spec.Children = append(spec.Children, child.ProcessID)
		}
	}
	if !spec.Valid() {
		return agent.ChildWaitSpec{}, errors.New("childcall: invalid wait request")
	}
	return spec, nil
}

func (b Batch) AcceptOpening(opened agent.ChildWaitOpened) (agent.WaitID, error) {
	if err := b.Validate(); err != nil {
		return agent.WaitID{}, err
	}
	if b.Phase() != PhaseAwaitingOpening || !opened.Valid() {
		return agent.WaitID{}, errors.New("childcall: opening is out of phase")
	}
	return opened.WaitID(), nil
}

// Complete validates the wait, boundary, count, order, and Process identities
// before returning the matching child indices. No earlier outcome is adopted on error.
func (b Batch) Complete(completed agent.ChildWaitSatisfied, key agent.WaitKey, boundary agent.ChildWaitBoundary, condition agent.ChildWaitCondition) ([]int, error) {
	spec, err := b.WaitSpec(key, boundary, condition)
	if err != nil {
		return nil, err
	}
	if b.Phase() != PhaseAwaitingCompletion {
		return nil, errors.New("childcall: completion is out of phase")
	}
	if !completed.Matches(b.WaitID, spec) {
		return nil, errors.New("childcall: completion does not match the declared wait")
	}
	return b.MatchOutcomes(completed.Outcomes())
}

// MatchOutcomes validates an ordered subset of unhandled child outcomes and
// returns the child each answers. Complete calls it after establishing the
// wait boundary.
func (b Batch) MatchOutcomes(outcomes []agent.ChildOutcome) ([]int, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	indices := make([]int, 0, len(outcomes))
	next := 0
	for _, outcome := range outcomes {
		for next < len(b.Children) && (b.Children[next].Done || b.Children[next].ProcessID != outcome.Result().ProcessID()) {
			next++
		}
		if next == len(b.Children) || !outcome.Valid() {
			return nil, fmt.Errorf("childcall: outcome %s does not match its declared child", outcome.Result().ProcessID())
		}
		indices = append(indices, next)
		next++
	}
	return indices, nil
}
