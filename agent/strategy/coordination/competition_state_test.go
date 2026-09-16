package coordination

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/Tangerg/scope/agent"
)

func TestCompetitionMergesOutcomesInCandidateOrder(t *testing.T) {
	starts, outcomes := competitionOutcomes(t, 6)
	execution := competitionExecution(t, starts)
	for _, batch := range [][]agent.ChildOutcome{
		{outcomes[1], outcomes[3], outcomes[5]},
		{outcomes[0], outcomes[2], outcomes[4]},
	} {
		execution.state.Phase = competitionWaiting
		execution.state.WaitID = new(competitionWaitID(t))
		signal := competitionCompletion(t, execution.state, batch)
		if _, err := execution.Step(t.Context(), []agent.Signal{signal}); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.EqualFunc(execution.state.Outcomes, outcomes, sameOutcome) {
		t.Fatal("merged outcomes changed candidate order or contents")
	}
	if !execution.state.result().Valid() {
		t.Fatal("complete competition result is invalid")
	}
}

func TestCompetitionRejectsInvalidOutcomeBatchesAtomically(t *testing.T) {
	starts, outcomes := competitionOutcomes(t, 4)
	for name, test := range map[string]struct {
		outcomes []agent.ChildOutcome
		cause    error
	}{
		"empty":            {nil, agent.ErrInvalidChildWait},
		"duplicate":        {[]agent.ChildOutcome{outcomes[0], outcomes[0]}, agent.ErrInvalidChildWait},
		"already observed": {[]agent.ChildOutcome{outcomes[0], outcomes[1]}, ErrInvalidProtocol},
		"unordered":        {[]agent.ChildOutcome{outcomes[2], outcomes[0]}, ErrInvalidProtocol},
		"foreign":          {[]agent.ChildOutcome{outcomes[0], outcomes[3]}, ErrInvalidProtocol},
	} {
		t.Run(name, func(t *testing.T) {
			execution := competitionExecution(t, starts[:3])
			execution.state.Outcomes = []agent.ChildOutcome{outcomes[1]}
			before, err := execution.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			signal := competitionCompletion(t, execution.state, test.outcomes)
			if transition, stepErr := execution.Step(t.Context(), []agent.Signal{signal}); !errors.Is(stepErr, test.cause) || transition.Valid() {
				t.Fatalf("invalid completion returned %v, %v; want %v", transition, stepErr, test.cause)
			}
			after, err := execution.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if string(after.Payload()) != string(before.Payload()) {
				t.Fatal("rejected batch changed execution state")
			}
		})
	}
}

func TestCompetitionRejectsForeignRetainedOutcomesOnRestore(t *testing.T) {
	starts, outcomes := competitionOutcomes(t, 3)
	execution := competitionExecution(t, starts[:2])
	execution.state.Outcomes = []agent.ChildOutcome{outcomes[2]}
	snapshot, err := execution.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := execution.definition.Restore(t.Context(), snapshot); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("foreign retained outcome accepted on restore: %v", err)
	}
	if execution.state.result().Valid() {
		t.Fatal("foreign retained outcome accepted as a public result")
	}
}

func competitionExecution(t *testing.T, starts []agent.ChildStartResult) *firstSuccessExecution {
	t.Helper()
	definition, err := NewFirstSuccess(FirstSuccessConfig{
		Name: "test.competition", Description: "Exercise ordered outcome adoption.", MaxCandidates: uint32(len(starts)),
		Accept: func(_ context.Context, _ agent.ChildOutcome) (bool, error) { return false, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	state := firstSuccessState{Phase: competitionWaiting, Starts: starts, WaitID: new(competitionWaitID(t))}
	for _, start := range starts {
		payload, err := agent.EncodePayload("input")
		if err != nil {
			t.Fatal(err)
		}
		state.Candidates = append(state.Candidates, agent.ChildSpec{Key: start.Key(), DeploymentRef: start.DeploymentRef(), Input: payload, Budget: agent.Budget{Steps: 16, Effects: 8, Signals: 16}})
	}
	if err := state.validate(definition.maxCandidates); err != nil {
		t.Fatal(err)
	}
	return &firstSuccessExecution{definition: definition, state: state}
}

func competitionWaitID(t *testing.T) agent.WaitID {
	t.Helper()
	id, err := agent.ParseWaitID("competition.wait")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func competitionCompletion(t *testing.T, state firstSuccessState, outcomes []agent.ChildOutcome) agent.Signal {
	t.Helper()
	spec, err := state.waitSpec()
	if err != nil {
		t.Fatal(err)
	}
	payload := struct {
		Operation string                  `json:"operation"`
		Key       agent.WaitKey           `json:"key"`
		Boundary  agent.ChildWaitBoundary `json:"boundary"`
		Outcomes  []agent.ChildOutcome    `json:"outcomes"`
	}{"child_wait_satisfied", spec.Key, spec.Boundary, outcomes}
	data, err := json.Marshal(struct {
		ID      string       `json:"id"`
		WaitID  agent.WaitID `json:"wait_id"`
		Payload any          `json:"payload"`
	}{"signal:engine:competition", *state.WaitID, payload})
	if err != nil {
		t.Fatal(err)
	}
	var signal agent.Signal
	if err := json.Unmarshal(data, &signal); err != nil {
		t.Fatal(err)
	}
	return signal
}

func sameOutcome(left, right agent.ChildOutcome) bool {
	return left.Key() == right.Key() && left.Result().ProcessID() == right.Result().ProcessID() &&
		left.Result().Status() == right.Result().Status()
}
