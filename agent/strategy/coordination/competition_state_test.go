package coordination

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"maps"
	"slices"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/internal/conformancetest"
)

func TestCompetitionRetainsStartDeclarationThroughRecovery(t *testing.T) {
	starts, _ := competitionOutcomes(t, 2)
	source := competitionExecution(t, starts)
	input, err := agent.EncodePayload(source.state.Candidates)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := source.definition.Start(input)
	if err != nil {
		t.Fatal(err)
	}
	for index, expected := range [][]string{{"candidates"}, {"candidates", "starts"}, {"candidates", "starts"}} {
		state, snapshotErr := execution.Snapshot()
		if snapshotErr != nil {
			t.Fatal(snapshotErr)
		}
		var fields map[string]json.RawMessage
		if decodeErr := jsonv2.Unmarshal(state.Payload(), &fields); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if got := slices.Sorted(maps.Keys(fields)); !slices.Equal(got, expected) {
			t.Fatalf("progress %d fields = %v, want %v", index, got, expected)
		}
		if index == 1 && string(fields["starts"]) != "[null,null]" {
			t.Fatalf("declaration before first receipt = %s, want one empty slot per candidate", fields["starts"])
		}
		execution, err = source.definition.Restore(t.Context(), state)
		if err != nil {
			t.Fatal(err)
		}
		var signals []agent.Signal
		if index > 0 {
			if _, err := execution.Step(t.Context(), nil); !errors.Is(err, ErrInvalidProtocol) {
				t.Fatalf("declared starts were reissued without receipts: %v", err)
			}
			data, encodeErr := jsonv2.Marshal(struct {
				ID      string                 `json:"id"`
				Payload agent.ChildStartResult `json:"payload"`
			}{"signal:engine:start", starts[index-1]})
			if encodeErr != nil {
				t.Fatal(encodeErr)
			}
			var signal agent.Signal
			if decodeErr := jsonv2.Unmarshal(data, &signal); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			signals = []agent.Signal{signal}
		}
		transition, stepErr := execution.Step(t.Context(), signals)
		if stepErr != nil {
			t.Fatal(stepErr)
		}
		wantEffects, wantConsumed := 2, uint32(0)
		if index > 0 {
			wantEffects, wantConsumed = index-1, 1
		}
		if transition.Kind() != agent.TransitionKindContinue || transition.ConsumedSignals() != wantConsumed || len(transition.Effects()) != wantEffects {
			t.Fatalf("progress %d transition = %v", index, transition)
		}
	}
}

func TestCompetitionMergesOutcomesInCandidateOrder(t *testing.T) {
	starts, outcomes := competitionOutcomes(t, 6)
	execution := competitionExecution(t, starts)
	for _, batch := range [][]agent.ChildOutcome{
		{outcomes[1], outcomes[3], outcomes[5]},
		{outcomes[0], outcomes[2], outcomes[4]},
	} {
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
	if _, err := execution.definition.Restore(t.Context(), snapshot); !errors.Is(err, ErrInvalidExecutionState) {
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
		Accept: func(_ context.Context, _ agent.ChildKey, _ agent.ChildOutcome) (bool, error) { return false, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	state := firstSuccessState{Starts: startSlots(starts), WaitID: new(competitionWaitID(t))}
	for index := range starts {
		payload, err := agent.EncodePayload("input")
		if err != nil {
			t.Fatal(err)
		}
		state.Candidates = append(state.Candidates, agent.ChildSpec{Key: candidateKey(t, index), DeploymentRef: benchmarkDeploymentRef(t), Input: payload, Budget: agent.Budget{Steps: agent.NewQuota(16), Effects: agent.NewQuota(8), Signals: agent.NewQuota(16)}})
	}
	if err := state.validate(t.Context(), definition.maxCandidates); err != nil {
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
	payload := struct {
		Operation string               `json:"operation"`
		Outcomes  []agent.ChildOutcome `json:"outcomes"`
	}{"child_wait_satisfied", outcomes}
	data, err := jsonv2.Marshal(struct {
		ID      string       `json:"id"`
		WaitID  agent.WaitID `json:"wait_id"`
		Payload any          `json:"payload"`
	}{"signal:engine:competition", *state.WaitID, payload})
	if err != nil {
		t.Fatal(err)
	}
	var signal agent.Signal
	if err := jsonv2.Unmarshal(data, &signal); err != nil {
		t.Fatal(err)
	}
	return signal
}

func sameOutcome(left, right agent.ChildOutcome) bool {
	return left.Result().ProcessID() == right.Result().ProcessID() &&
		left.Result().Status() == right.Result().Status()
}

func TestRestoreStopsBetweenCandidates(t *testing.T) {
	starts, _ := competitionOutcomes(t, 2)
	execution := competitionExecution(t, starts)
	state := execution.state
	state.Candidates[1] = agent.ChildSpec{}
	ctx, cancel := conformancetest.CancelAfterCheck(t.Context(), 2)
	defer cancel()
	if err := state.validate(ctx, 2); !errors.Is(err, context.Canceled) {
		t.Fatalf("candidate validation = %v, want cancellation before malformed second candidate", err)
	}
}

func startSlots(starts []agent.ChildStartResult) []*agent.ChildStartResult {
	slots := make([]*agent.ChildStartResult, len(starts))
	for index := range starts {
		slots[index] = &starts[index]
	}
	return slots
}

func TestCompetitionStartSlotsMatchCandidatesInOrder(t *testing.T) {
	starts, _ := competitionOutcomes(t, 3)
	execution := competitionExecution(t, starts)
	for name, slots := range map[string][]*agent.ChildStartResult{
		"missing slot":           startSlots(starts)[:2],
		"receipt after gap":      {&starts[0], nil, &starts[2]},
		"declared without slots": {},
	} {
		state := execution.state
		state.Starts, state.WaitID = slots, nil
		err := state.validate(t.Context(), execution.definition.maxCandidates)
		if name == "declared without slots" {
			if err != nil || state.phase() != competitionReady {
				t.Fatalf("empty slots must mean an undeclared competition: phase=%d err=%v", state.phase(), err)
			}
			continue
		}
		if !errors.Is(err, ErrInvalidExecutionState) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
}
