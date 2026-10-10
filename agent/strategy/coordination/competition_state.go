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
	// Completed marks a finished competition. The Engine owns its
	// FirstSuccessResult, so the state keeps neither results nor a winner.
	Completed bool `json:"completed,omitzero"`
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
	case f.Completed:
		return competitionCompleted
	case len(f.Results) == 0:
		return competitionReady
	case f.admitted() < len(f.Candidates):
		return competitionAwaitingStarts
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
	// Receipts fill results in candidate order, so non-nil results must form a
	// prefix. One pass checks that no receipt follows an empty slot, avoiding an
	// admitted() rescan per slot.
	sawEmpty := false
	for index, result := range f.Results {
		if result == nil {
			sawEmpty = true
			continue
		}
		if sawEmpty {
			return fmt.Errorf("%w: start receipt follows an empty result", ErrInvalidExecutionState)
		}
		if !result.Valid() {
			return fmt.Errorf("%w: candidate %d result is invalid", ErrInvalidExecutionState, index)
		}
	}
	for _, candidate := range f.Candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !candidate.Valid() {
			return fmt.Errorf("%w: invalid candidate", ErrInvalidExecutionState)
		}
	}
	// The batch owns key uniqueness across candidates.
	if err := f.batch().Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
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
		if !f.anyRemaining() {
			return fmt.Errorf("%w: a competition without running candidates has completed", ErrInvalidExecutionState)
		}
	case competitionCompleted:
		if len(f.Results) != 0 || f.WaitID != nil {
			return fmt.Errorf("%w: completed competition repeats its Output", ErrInvalidExecutionState)
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

// anyRemaining reports whether any admitted competitor is still running.
func (f firstSuccessState) anyRemaining() bool {
	for _, result := range f.Results {
		if result != nil && result.running() {
			return true
		}
	}
	return false
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

func (f firstSuccessState) result(winner *uint32) FirstSuccessResult {
	result := FirstSuccessResult{Candidates: make([]CandidateResult, len(f.Results))}
	for index, candidate := range f.Results {
		if candidate != nil {
			result.Candidates[index] = *candidate
		}
	}
	result.Winner = winner
	return result
}
