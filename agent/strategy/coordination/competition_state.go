package coordination

import (
	"fmt"
	"slices"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/internal/childcall"
)

type competitionPhase string

const (
	competitionReady          competitionPhase = "ready"
	competitionAwaitingStarts competitionPhase = "awaiting_starts"
	competitionAwaitingOpen   competitionPhase = "awaiting_open"
	competitionWaiting        competitionPhase = "waiting"
	competitionCompleted      competitionPhase = "completed"
)

type firstSuccessState struct {
	Phase      competitionPhase         `json:"phase"`
	Candidates []agent.ChildSpec        `json:"candidates"`
	Starts     []agent.ChildStartResult `json:"starts,omitempty"`
	Outcomes   []agent.ChildOutcome     `json:"outcomes,omitempty"`
	WaitID     *agent.WaitID            `json:"wait_id,omitzero"`
	Winner     *agent.ChildKey          `json:"winner,omitzero"`
}

func (f firstSuccessState) validate(maxCandidates uint32) error {
	if len(f.Candidates) == 0 || uint64(len(f.Candidates)) > uint64(maxCandidates) || len(f.Starts) > len(f.Candidates) {
		return fmt.Errorf("%w: candidate or start count exceeds its bound", ErrInvalidState)
	}
	for index, candidate := range f.Candidates {
		if !candidate.Valid() {
			return fmt.Errorf("%w: invalid candidate", ErrInvalidState)
		}
		for _, previous := range f.Candidates[:index] {
			if candidate.Key == previous.Key {
				return fmt.Errorf("%w: duplicate candidate key", ErrInvalidState)
			}
		}
	}
	if len(f.Starts) > 0 {
		pending := f
		pending.Starts, pending.Outcomes, pending.WaitID = nil, nil, nil
		if _, err := pending.batch().AcceptStarts(f.Starts); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidState, err)
		}
	}
	if !validObservedOutcomes(f.Starts, f.Outcomes) {
		return fmt.Errorf("%w: observed outcomes disagree with candidate order or identities", ErrInvalidState)
	}
	if f.Phase != competitionCompleted && f.Winner != nil {
		return fmt.Errorf("%w: unfinished competition contains a winner", ErrInvalidState)
	}
	return f.validatePhase()
}

func (f firstSuccessState) validatePhase() error {
	switch f.Phase {
	case competitionReady:
		if len(f.Starts) != 0 || len(f.Outcomes) != 0 || f.WaitID != nil {
			return fmt.Errorf("%w: ready competition retains progress", ErrInvalidState)
		}
	case competitionAwaitingStarts:
		if len(f.Starts) >= len(f.Candidates) {
			return fmt.Errorf("%w: awaiting starts has no pending candidate", ErrInvalidState)
		}
		if len(f.Outcomes) != 0 || f.WaitID != nil {
			return fmt.Errorf("%w: outcomes or wait precede completed starts", ErrInvalidState)
		}
	case competitionAwaitingOpen, competitionWaiting:
		if len(f.Starts) != len(f.Candidates) {
			return fmt.Errorf("%w: competition wait precedes completed starts", ErrInvalidState)
		}
		if len(f.remaining()) == 0 {
			return fmt.Errorf("%w: competition wait has no remaining candidate", ErrInvalidState)
		}
		if f.Phase == competitionAwaitingOpen && f.WaitID != nil {
			return fmt.Errorf("%w: unopened wait already has a WaitID", ErrInvalidState)
		}
		if f.Phase == competitionWaiting && (f.WaitID == nil || !f.WaitID.Valid()) {
			return fmt.Errorf("%w: waiting competition requires a valid WaitID", ErrInvalidState)
		}
	case competitionCompleted:
		if len(f.Starts) != len(f.Candidates) || f.WaitID != nil {
			return fmt.Errorf("%w: completed competition retains pending starts or wait", ErrInvalidState)
		}
		if !f.result().Valid() {
			return fmt.Errorf("%w: completed competition has an invalid result", ErrInvalidState)
		}
	default:
		return fmt.Errorf("%w: unknown competition phase %q", ErrInvalidState, f.Phase)
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
		child.Key, child.Deployment = candidate.Key, candidate.DeploymentRef
		if index < len(f.Starts) {
			id, started := f.Starts[index].ProcessID()
			child.ProcessID, child.Done = id, !started || observedProcess(f.Outcomes, id)
		}
	}
	return batch
}

func (f firstSuccessState) remaining() []agent.ProcessID {
	var children []agent.ProcessID
	for _, started := range f.Starts {
		if id, present := started.ProcessID(); present && !observedProcess(f.Outcomes, id) {
			children = append(children, id)
		}
	}
	return children
}

func (f firstSuccessState) waitSpec() (agent.ChildWaitSpec, error) {
	key, err := agent.ParseWaitKey(fmt.Sprintf("coordination.first_success.%d", len(f.Outcomes)))
	if err != nil {
		return agent.ChildWaitSpec{}, err
	}
	return f.batch().WaitSpec(key, agent.ChildWaitBoundaryResult, agent.AnyChild())
}

func (f *firstSuccessState) recordOutcomes(outcomes []agent.ChildOutcome) error {
	if len(outcomes) == 0 || !validObservedOutcomes(f.Starts, outcomes) {
		return fmt.Errorf("%w: candidate outcomes are empty, unordered, or foreign", ErrInvalidProtocol)
	}
	// Both collections follow Starts. Merge in that order without searching
	// the full start list for each sorting comparison.
	merged := make([]agent.ChildOutcome, 0, len(f.Outcomes)+len(outcomes))
	prior, incoming := 0, 0
	for _, started := range f.Starts {
		hasPrior := prior < len(f.Outcomes) && outcomeMatchesStart(f.Outcomes[prior], started)
		hasIncoming := incoming < len(outcomes) && outcomeMatchesStart(outcomes[incoming], started)
		if hasPrior && hasIncoming {
			return fmt.Errorf("%w: candidate outcome was already observed", ErrInvalidProtocol)
		}
		if hasPrior {
			merged = append(merged, f.Outcomes[prior])
			prior++
		}
		if hasIncoming {
			merged = append(merged, outcomes[incoming])
			incoming++
		}
	}
	if prior != len(f.Outcomes) || incoming != len(outcomes) {
		return fmt.Errorf("%w: candidate outcomes could not be merged", ErrInvalidProtocol)
	}
	f.Outcomes = merged
	return nil
}

func (f firstSuccessState) result() FirstSuccessResult {
	outcomes := make([]agent.ChildOutcome, len(f.Outcomes))
	copy(outcomes, f.Outcomes)
	result := FirstSuccessResult{Starts: slices.Clone(f.Starts), Outcomes: outcomes}
	if f.Winner != nil {
		winner := *f.Winner
		result.Winner = &winner
	}
	return result
}
