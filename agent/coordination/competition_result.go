package coordination

import "github.com/Tangerg/scope/agent/run"

// FirstSuccessResult reports every candidate once the competition drains.
// Winner, when present, indexes a candidate whose Process completed.
type FirstSuccessResult struct {
	Winner     *uint32           `json:"winner,omitzero"`
	Candidates []CandidateResult `json:"candidates"`
}

// CandidateResult holds exactly one of a candidate's termination and the
// rejection of its start.
type CandidateResult struct {
	Outcome   *run.Termination `json:"outcome,omitzero"`
	Rejection *run.Failure     `json:"rejection,omitzero"`
}
