package coordination

import (
	"context"
	"fmt"

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
	// Declaring the starts creates one result per candidate; a nil result
	// awaits its start receipt. Before declaration there are no results.
	Results []*CandidateResult `json:"results,omitempty"`
	WaitID  *agent.WaitID      `json:"wait_id,omitzero"`
	// Winner is the accepted candidate's index in request order.
	Winner *uint32 `json:"winner,omitzero"`
}

// admitted counts the results whose start receipts arrived; receipts fill
// the results in candidate order.
func (f firstSuccessState) admitted() int {
	for index, result := range f.Results {
		if result == nil {
			return index
		}
	}
	return len(f.Results)
}

func (f firstSuccessState) observed() int {
	count := 0
	for _, result := range f.Results {
		if result != nil && result.Outcome != nil {
			count++
		}
	}
	return count
}

func (f firstSuccessState) phase() competitionPhase {
	switch {
	case len(f.Results) == 0:
		return competitionReady
	case f.admitted() < len(f.Candidates):
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
	if len(f.Candidates) == 0 || uint64(len(f.Candidates)) > uint64(maxCandidates) ||
		len(f.Results) != 0 && len(f.Results) != len(f.Candidates) {
		return fmt.Errorf("%w: candidate or result count is invalid", ErrInvalidExecutionState)
	}
	for index, result := range f.Results {
		if index >= f.admitted() && result != nil {
			return fmt.Errorf("%w: start receipt follows an empty result", ErrInvalidExecutionState)
		}
		if result != nil && !result.Valid() {
			return fmt.Errorf("%w: candidate %d result is invalid", ErrInvalidExecutionState, index)
		}
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
	if err := f.batch().Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	if f.phase() != competitionCompleted && f.Winner != nil {
		return fmt.Errorf("%w: unfinished competition contains a winner", ErrInvalidExecutionState)
	}
	return f.validatePhase()
}

func (f firstSuccessState) validatePhase() error {
	switch f.phase() {
	case competitionReady, competitionAwaitingStarts:
		if f.observed() != 0 || f.WaitID != nil {
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
	for index, candidate := range f.Candidates {
		child := &batch.Children[index]
		child.Key = candidate.Key
		if index < len(f.Results) && f.Results[index] != nil {
			result := f.Results[index]
			child.ProcessID, _ = result.processID()
			child.Done = !result.running()
		}
	}
	return batch
}

func (f firstSuccessState) remaining() []agent.ProcessID {
	var children []agent.ProcessID
	for _, result := range f.Results {
		if result != nil && result.running() {
			id, _ := result.Start.ProcessID()
			children = append(children, id)
		}
	}
	return children
}

// Every satisfied wait adds an outcome, so the outcome count keys a fresh wait.
func (f firstSuccessState) waitSpec() (agent.ChildWaitSpec, error) {
	key, err := agent.ParseWaitKey(fmt.Sprintf("%s.%d", firstSuccessWaitKeyPrefix, f.observed()))
	if err != nil {
		return agent.ChildWaitSpec{}, err
	}
	return f.batch().WaitSpec(key, agent.ChildWaitBoundaryResult, agent.AnyChild())
}

// recordOutcomes replaces each observed candidate's start receipt with its
// outcome; indices are the candidates Complete matched.
func (f *firstSuccessState) recordOutcomes(indices []int, outcomes []agent.ChildOutcome) {
	for offset, index := range indices {
		f.Results[index] = &CandidateResult{Outcome: &outcomes[offset]}
	}
}

func (f firstSuccessState) result() FirstSuccessResult {
	result := FirstSuccessResult{Candidates: make([]CandidateResult, len(f.Results))}
	for index, candidate := range f.Results {
		if candidate != nil {
			result.Candidates[index] = *candidate
		}
	}
	if f.Winner != nil {
		result.Winner = new(*f.Winner)
	}
	return result
}
