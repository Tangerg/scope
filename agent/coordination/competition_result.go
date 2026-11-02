package coordination

import "github.com/Tangerg/scope/agent/run"

type FirstSuccessResult struct {
	Winner     *uint32           `json:"winner,omitzero"`
	Candidates []CandidateResult `json:"candidates"`
}

func (f FirstSuccessResult) Valid() bool {
	if len(f.Candidates) == 0 {
		return false
	}
	for _, candidate := range f.Candidates {
		if !candidate.Valid() {
			return false
		}
	}
	return f.Winner == nil || uint64(*f.Winner) < uint64(len(f.Candidates)) && f.Candidates[*f.Winner].Outcome != nil && f.Candidates[*f.Winner].Outcome.Status() == run.StatusCompleted
}

type CandidateResult struct {
	Outcome   *run.Termination `json:"outcome,omitzero"`
	Rejection *run.Failure     `json:"rejection,omitzero"`
}

func (c CandidateResult) Valid() bool {
	if (c.Outcome == nil) == (c.Rejection == nil) {
		return false
	}
	if c.Outcome != nil {
		return c.Outcome.Valid()
	}
	return c.Rejection.Valid()
}
