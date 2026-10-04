package coordination

import (
	agent "github.com/Tangerg/scope/agent"
)

// FirstSuccessResult answers each candidate in request order. Candidates
// still running when a winner is published keep their start receipt.
type FirstSuccessResult struct {
	// Winner is the accepted candidate's index in request order.
	Winner     *uint32           `json:"winner,omitzero"`
	Candidates []CandidateResult `json:"candidates"`
}

func (f FirstSuccessResult) Valid() bool {
	if len(f.Candidates) == 0 {
		return false
	}
	running := false
	for _, candidate := range f.Candidates {
		if !candidate.Valid() {
			return false
		}
		running = running || candidate.running()
	}
	if f.Winner == nil {
		// Without a winner the competition ends only after every child finished.
		return !running
	}
	if uint64(*f.Winner) >= uint64(len(f.Candidates)) {
		return false
	}
	winner := f.Candidates[*f.Winner].Outcome
	return winner != nil && winner.Result().Status() == agent.StatusCompleted
}

// CandidateResult holds one candidate's kernel facts: its start receipt after
// a failed admission or while the child runs, replaced by the child's
// terminal outcome once observed. Exactly one is present, and the outcome's
// Result names the child.
type CandidateResult struct {
	Start   *agent.ChildStartResult `json:"start,omitzero"`
	Outcome *agent.ChildOutcome     `json:"outcome,omitzero"`
}

func (c CandidateResult) Valid() bool {
	switch {
	case c.Start != nil && c.Outcome == nil:
		return c.Start.Valid()
	case c.Start == nil && c.Outcome != nil:
		_, drained := c.Outcome.SubtreeUnresolvedEffects()
		return c.Outcome.Valid() && !drained
	default:
		return false
	}
}

func (c CandidateResult) running() bool {
	if c.Start == nil {
		return false
	}
	_, started := c.Start.ProcessID()
	return started
}

// processID names the candidate's child while it runs or after it finished.
func (c CandidateResult) processID() (agent.ProcessID, bool) {
	if c.Outcome != nil {
		return c.Outcome.Result().ProcessID(), true
	}
	return c.Start.ProcessID()
}
