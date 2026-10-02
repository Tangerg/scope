package coordination

import (
	"context"
	"fmt"
	"slices"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/internal/childcall"
)

const firstSuccessWaitKeyPrefix = "coordination.first_success"

type competitionPhase uint8

const (
	competitionReady competitionPhase = iota
	competitionAwaitingStarts
	competitionAwaitingOpen
	competitionWaiting
	competitionCompleted
)

type firstSuccessState struct {
	Candidates []agent.ChildSpec `json:"candidates"`
	// Nil precedes declaration; an explicit empty slice records declared starts
	// before the first receipt. Non-empty receipts carry that fact thereafter.
	Starts   []agent.ChildStartResult `json:"starts,omitzero"`
	Outcomes []agent.ChildOutcome     `json:"outcomes,omitempty"`
	WaitID   *agent.WaitID            `json:"wait_id,omitzero"`
	Winner   *agent.ChildKey          `json:"winner,omitzero"`
}

func (f firstSuccessState) phase() competitionPhase {
	switch {
	case f.Starts == nil:
		return competitionReady
	case len(f.Starts) < len(f.Candidates):
		return competitionAwaitingStarts
	case f.Winner != nil || len(f.remaining()) == 0:
		return competitionCompleted
	case f.WaitID != nil:
		return competitionWaiting
	default:
		return competitionAwaitingOpen
	}
}

func (f firstSuccessState) validate(ctx context.Context, maxCandidates uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(f.Candidates) == 0 || uint64(len(f.Candidates)) > uint64(maxCandidates) || len(f.Starts) > len(f.Candidates) {
		return fmt.Errorf("%w: candidate or start count exceeds its bound", ErrInvalidExecutionState)
	}
	keys := make(map[agent.ChildKey]struct{}, len(f.Candidates))
	for _, candidate := range f.Candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !candidate.Valid() {
			return fmt.Errorf("%w: invalid candidate", ErrInvalidExecutionState)
		}
		if _, duplicate := keys[candidate.Key]; duplicate {
			return fmt.Errorf("%w: duplicate candidate key", ErrInvalidExecutionState)
		}
		keys[candidate.Key] = struct{}{}
	}
	if len(f.Starts) > 0 {
		pending := f
		pending.Starts, pending.Outcomes, pending.WaitID = nil, nil, nil
		if _, err := pending.batch().AcceptStarts(f.Starts); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
		}
	}
	unobserved := f
	unobserved.Outcomes, unobserved.WaitID = nil, nil
	if _, err := unobserved.batch().MatchOutcomes(f.Outcomes); err != nil {
		return fmt.Errorf("%w: observed outcomes: %w", ErrInvalidExecutionState, err)
	}
	if f.phase() != competitionCompleted && f.Winner != nil {
		return fmt.Errorf("%w: unfinished competition contains a winner", ErrInvalidExecutionState)
	}
	return f.validatePhase()
}

func (f firstSuccessState) validatePhase() error {
	switch f.phase() {
	case competitionReady, competitionAwaitingStarts:
		if len(f.Outcomes) != 0 || f.WaitID != nil {
			return fmt.Errorf("%w: outcomes or wait precede completed starts", ErrInvalidExecutionState)
		}
	case competitionAwaitingOpen, competitionWaiting:
		if f.WaitID != nil && !f.WaitID.Valid() {
			return fmt.Errorf("%w: competition WaitID is invalid", ErrInvalidExecutionState)
		}
	case competitionCompleted:
		if f.WaitID != nil {
			return fmt.Errorf("%w: completed competition retains a wait", ErrInvalidExecutionState)
		}
		if !f.result().Valid() {
			return fmt.Errorf("%w: completed competition has an invalid result", ErrInvalidExecutionState)
		}
	}
	return nil
}

func (f firstSuccessState) batch() childcall.Batch {
	batch := childcall.Batch{Children: make([]childcall.Child, len(f.Candidates))}
	if f.WaitID != nil {
		batch.WaitID = *f.WaitID
	}
	next := 0
	for index, candidate := range f.Candidates {
		child := &batch.Children[index]
		child.Key, child.Deployment = candidate.Key, candidate.DeploymentRef
		if index < len(f.Starts) {
			id, started := f.Starts[index].ProcessID()
			child.ProcessID, child.Done = id, !started
			if started && next < len(f.Outcomes) && f.Outcomes[next].Result().ProcessID() == id {
				child.Done = true
				next++
			}
		}
	}
	return batch
}

func (f firstSuccessState) remaining() []agent.ProcessID {
	var children []agent.ProcessID
	for _, child := range f.batch().Children {
		if child.ProcessID.Valid() && !child.Done {
			children = append(children, child.ProcessID)
		}
	}
	return children
}

// Every satisfied wait adds an outcome, so the outcome count keys a fresh wait.
func (f firstSuccessState) waitSpec() (agent.ChildWaitSpec, error) {
	key, err := agent.ParseWaitKey(fmt.Sprintf("%s.%d", firstSuccessWaitKeyPrefix, len(f.Outcomes)))
	if err != nil {
		return agent.ChildWaitSpec{}, err
	}
	return f.batch().WaitSpec(key, agent.ChildWaitBoundaryResult, agent.AnyChild())
}

func (f *firstSuccessState) recordOutcomes(indices []int, outcomes []agent.ChildOutcome) {
	// indices are ordered, previously unobserved candidates as returned by Complete.
	merged := make([]agent.ChildOutcome, 0, len(f.Outcomes)+len(outcomes))
	prior, incoming := 0, 0
	for index, started := range f.Starts {
		if incoming < len(indices) && indices[incoming] == index {
			merged = append(merged, outcomes[incoming])
			incoming++
		} else if prior < len(f.Outcomes) && f.Outcomes[prior].Key() == started.Key() {
			merged = append(merged, f.Outcomes[prior])
			prior++
		}
	}
	f.Outcomes = merged
}

func (f firstSuccessState) result() FirstSuccessResult {
	// FirstSuccessResult requires non-nil Outcomes, which slices.Clone(nil) would not produce.
	outcomes := make([]agent.ChildOutcome, len(f.Outcomes))
	copy(outcomes, f.Outcomes)
	result := FirstSuccessResult{Starts: slices.Clone(f.Starts), Outcomes: outcomes}
	if f.Winner != nil {
		result.Winner = new(*f.Winner)
	}
	return result
}
