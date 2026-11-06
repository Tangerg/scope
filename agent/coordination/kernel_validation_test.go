package coordination

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/internal/strategy/child"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

func TestCoordinationConstructionRejectsInvalidNamesSchemasAndLimits(t *testing.T) {
	compiler := conformancetest.Compiler(t)
	schema := require(jsonschema.For[string]())
	for _, config := range []Config{
		{Name: "invalid name", Description: "Wait.", Policy: Deadline{}},
		{Name: "test.deadline", Policy: Deadline{}},
		{Name: "test.gate", Description: "Ask.", Policy: InputGate{AnswerSchema: schema}},
		{Name: "test.gate", Description: "Ask.", Policy: InputGate{RequestSchema: schema, AnswerSchema: schema, MaxAnswerBytes: -1}},
		{Name: "invalid name", Description: "Race.", Policy: FirstSuccess{MaxCandidates: 1, Revision: "accept", Accept: func(context.Context, run.ChildKey, run.Termination) (bool, error) { return true, nil }}},
	} {
		if _, err := Compile[run.Payload, run.Payload](compiler, config); err == nil {
			t.Fatalf("invalid construction admitted: %+v", config)
		}
	}
}

func TestInputGateRejectsUnaddressedMessagesAndDropsCompletedRequest(t *testing.T) {
	schema := require(jsonschema.For[string]())
	program := require(Compile[string, string](conformancetest.Compiler(t), Config{Name: "test.gate", Description: "Ask once.", Policy: InputGate{RequestSchema: schema, AnswerSchema: schema}}))
	definition := program.Definition()
	if _, err := definition.Start(require(run.EncodePayload(12))); err == nil {
		t.Fatal("request violated its declared schema")
	}
	execution := require(definition.Start(require(run.EncodePayload("question"))))
	initial := require(execution.Snapshot())
	key := require(run.ParseWaitKey(inputGateWaitKey))
	answer := run.Signal{ID: require(run.ParseSignalID("answer")), Wait: key, Payload: require(run.EncodePayload("yes"))}
	if transition, err := execution.Step(t.Context(), turn(1, answer)); transition.Valid() || !errors.Is(err, ErrInvalidProtocol) {
		t.Fatalf("premature answer advanced the gate: %v, %v", transition, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := execution.Step(ctx, turn(1)); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled gate advanced", err)
	}
	if after := require(execution.Snapshot()); !bytes.Equal(initial.Payload(), after.Payload()) {
		t.Fatal("rejected initial turn changed the request")
	}
	require(execution.Step(t.Context(), turn(1)))
	waiting := require(execution.Snapshot())
	wrong := answer
	wrong.Wait = require(run.ParseWaitKey("other"))
	for _, messages := range [][]run.Message{nil, {wrong}, {answer, answer}, {run.Completion{Effect: turn(1).EffectID(0), Outcome: settled("yes")}}} {
		restored := require(definition.Restore(t.Context(), waiting))
		if transition, err := restored.Step(t.Context(), turn(2, messages...)); transition.Valid() || !errors.Is(err, ErrInvalidProtocol) {
			t.Fatalf("unaddressed answer advanced the gate: %v, %v", transition, err)
		}
		if after := require(restored.Snapshot()); !bytes.Equal(waiting.Payload(), after.Payload()) {
			t.Fatal("rejected answer changed the published wait")
		}
	}
	completed := require(execution.Step(t.Context(), turn(2, answer)))
	output, present := completed.Output()
	if !present || !output.Equal(answer.Payload) {
		t.Fatal("completion changed the admitted answer")
	}
	state := require(execution.Snapshot())
	retained := require(state.Decode[inputGateState](inputGateStateKind))
	if retained.Request.Valid() {
		t.Fatal("completed gate retained a second copy of its question")
	}
	restored := require(definition.Restore(t.Context(), state))
	if _, err := restored.Step(t.Context(), turn(3)); !errors.Is(err, ErrInvalidProtocol) {
		t.Fatal("completed gate advanced", err)
	}
	conformancetest.CheckRestoreCancellation(t, definition, waiting)
	for _, raw := range []string{`{"phase":"unknown"}`, `{"phase":"waiting","request":42}`, `{"phase":"completed","request":"question"}`} {
		state := require(run.ParseExecutionState(inputGateStateKind, []byte(raw)))
		if _, err := definition.Restore(t.Context(), state); !errors.Is(err, ErrInvalidExecutionState) {
			t.Fatalf("invalid gate state lost its owning classification: %s, %v", raw, err)
		}
	}
}

func TestDeadlineValidatesRestoredPhaseAndExactTimerCompletion(t *testing.T) {
	program := require(Compile[time.Time, time.Time](conformancetest.Compiler(t), Config{Name: "test.deadline", Description: "Wait for one absolute instant.", Policy: Deadline{}}))
	definition := program.Definition()
	deadline := time.Date(2027, time.January, 2, 3, 4, 5, 0, time.UTC)
	for _, value := range []any{time.Time{}, 42, "not a date"} {
		if _, err := definition.Start(require(run.EncodePayload(value))); err == nil {
			t.Fatalf("invalid deadline accepted: %v", value)
		}
	}
	execution := require(definition.Start(require(run.EncodePayload(deadline))))
	initial := require(execution.Snapshot())
	external := run.Signal{ID: require(run.ParseSignalID("external")), Payload: require(run.EncodePayload("unexpected"))}
	if _, err := execution.Step(t.Context(), turn(1, external)); !errors.Is(err, ErrInvalidProtocol) {
		t.Fatal("deadline accepted external input", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := execution.Step(ctx, turn(1)); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled timer preparation advanced", err)
	}
	if after := require(execution.Snapshot()); !bytes.Equal(initial.Payload(), after.Payload()) {
		t.Fatal("rejected preparation changed the deadline")
	}
	first := require(execution.Step(t.Context(), turn(1)))
	if len(first.Effects()) != 1 || first.Kind() != run.TransitionKindYield {
		t.Fatal("deadline did not prepare one timer")
	}
	waiting := require(execution.Snapshot())
	conformancetest.CheckRestoreCancellation(t, definition, waiting)
	for _, sample := range []deadlineState{
		{Phase: deadlineReady},
		{Deadline: deadline, Phase: "unknown"},
		{Deadline: deadline, Phase: deadlineAwaiting},
		{Deadline: deadline, Phase: deadlineReady, Effect: new(turn(1).EffectID(0))},
		{Deadline: deadline, Phase: deadlineCompleted, Effect: new(turn(1).EffectID(0))},
	} {
		state := require(run.EncodeExecutionState(deadlineStateKind, sample))
		if _, err := definition.Restore(t.Context(), state); !errors.Is(err, ErrInvalidExecutionState) {
			t.Fatalf("inconsistent timer phase admitted: %+v, %v", sample, err)
		}
	}
	for _, messages := range [][]run.Message{
		nil, {external}, {run.Completion{Effect: turn(1).EffectID(1), Outcome: settled(timerReached{})}},
		{run.Completion{Effect: turn(1).EffectID(0)}},
		{run.Completion{Effect: turn(1).EffectID(0), Outcome: require(run.NewSettlement(run.SettlementStatusUnknown, []byte(`null`)))}},
		{run.Completion{Effect: turn(1).EffectID(0), Outcome: settled("not a reached receipt")}},
	} {
		restored := require(definition.Restore(t.Context(), waiting))
		if transition, err := restored.Step(t.Context(), turn(2, messages...)); transition.Valid() || !errors.Is(err, ErrInvalidProtocol) {
			t.Fatalf("unrelated timer evidence advanced: %v, %v", transition, err)
		}
	}
	failed := require(definition.Restore(t.Context(), waiting))
	failure := require(failed.Step(t.Context(), turn(2, run.Completion{Effect: turn(1).EffectID(0), Outcome: require(timerFailureSettlement(errors.New("timer interrupted")))})))
	diagnostic, failedStep := failure.Failure()
	if !failedStep || diagnostic.Code() != failureCodeDeadlineInterrupted {
		t.Fatal("timer failure lost its classified cause")
	}
	restored := require(definition.Restore(t.Context(), waiting))
	completed := require(restored.Step(t.Context(), turn(2, run.Completion{Effect: turn(1).EffectID(0), Outcome: settled(timerReached{})})))
	output, ok := completed.Output()
	if !ok || !require(output.Decode[time.Time]()).Equal(deadline) {
		t.Fatal("timer completion changed its absolute deadline")
	}
	done := require(definition.Restore(t.Context(), require(restored.Snapshot())))
	if _, err := done.Step(t.Context(), turn(3)); !errors.Is(err, ErrInvalidProtocol) {
		t.Fatal("completed deadline restarted", err)
	}
}

func TestTimerProtocolRejectionAndCancellationAreDefinite(t *testing.T) {
	dispatcher := deadlineDispatcher{}
	if _, err := dispatcher.Dispatch(t.Context(), run.EffectRequest{}, nil); !errors.Is(err, ErrInvalidProtocol) {
		t.Fatal("invalid request was dispatched", err)
	}
	if dispatcher.Policy(run.Effect{}).Replay != run.ReplayPolicyNever {
		t.Fatal("invalid timer authorized replay")
	}
	for _, raw := range []string{`null`, `{}`, `{"deadline":"not a date"}`, `{"deadline":"2027-01-02T03:04:05Z","unexpected":true}`} {
		effect := require(run.NewDispatcherEffect([]byte(raw)))
		if dispatcher.Policy(effect).Replay != run.ReplayPolicyNever {
			t.Fatal("malformed timer authorized replay")
		}
		conformancetest.CheckDispatcherRejection(t, dispatcher, effect)
	}
	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		runtime := conformancetest.Runtime(t, run.Config{})
		program := require(Compile[time.Time, time.Time](runtime.Compiler(), Config{Name: "test.cancel_timer", Description: "Cancel an owned timer.", Policy: Deadline{}}))
		handle := require(runtime.Start(t.Context(), program, run.StartRequest[time.Time]{ID: run.StartID{Scope: "timer", Key: "cancel"}, Input: now.Add(time.Hour)}))
		synctest.Wait()
		if err := handle.Cancel(t.Context(), "cancel before deadline"); err != nil {
			t.Fatal(err)
		}
		result := require(handle.Await(t.Context()))
		if result.Termination().Status() != run.StatusCanceled || !time.Now().Equal(now) {
			t.Fatal("cancellation waited for the deadline or became uncertain")
		}
	})
}

func TestCompetitionResultRequiresOneOutcomePerCandidate(t *testing.T) {
	termination := ended("winner")
	failure := require(run.NewFailure(run.FailureKindContract, "test.rejected", "candidate rejected"))
	for _, sample := range []struct {
		result FirstSuccessResult
		valid  bool
	}{
		{FirstSuccessResult{}, false},
		{FirstSuccessResult{Candidates: []CandidateResult{{}}}, false},
		{FirstSuccessResult{Candidates: []CandidateResult{{Outcome: new(run.Termination)}}}, false},
		{FirstSuccessResult{Candidates: []CandidateResult{{Outcome: &termination, Rejection: &failure}}}, false},
		{FirstSuccessResult{Candidates: []CandidateResult{{Rejection: &failure}}}, true},
		{FirstSuccessResult{Winner: new(uint32(0)), Candidates: []CandidateResult{{Rejection: &failure}}}, false},
		{FirstSuccessResult{Winner: new(uint32(1)), Candidates: []CandidateResult{{Outcome: &termination}}}, false},
		{FirstSuccessResult{Winner: new(uint32(0)), Candidates: []CandidateResult{{Outcome: &termination}}}, true},
	} {
		if sample.result.Valid() != sample.valid {
			t.Fatalf("contradictory result classification: %+v", sample)
		}
	}
}

func TestCompetitionRestorationRejectsContradictoryPendingState(t *testing.T) {
	compiler := conformancetest.Compiler(t)
	candidate := require(Compile[time.Time, time.Time](compiler, Config{Name: "test.candidate", Description: "Wait.", Policy: Deadline{}}))
	program := require(Compile[[]run.ChildSpec, FirstSuccessResult](compiler, Config{Name: "test.competition", Description: "Choose a candidate.", Policy: FirstSuccess{MaxCandidates: 2, Revision: "accept", Accept: func(context.Context, run.ChildKey, run.Termination) (bool, error) { return true, nil }}}))
	definition := program.Definition()
	a := run.ChildSpec{Key: require(run.ParseChildKey("a")), ProgramRef: candidate.ProgramRef(), Input: require(run.EncodePayload(time.Now()))}
	b := a
	b.Key = require(run.ParseChildKey("b"))
	for _, candidates := range [][]run.ChildSpec{nil, {a, a}, {a, b, a}} {
		if _, err := definition.Start(require(run.EncodePayload(candidates))); err == nil {
			t.Fatal("invalid competition candidates accepted")
		}
	}
	execution := require(definition.Start(require(run.EncodePayload([]run.ChildSpec{a, b}))))
	initial := require(execution.Snapshot())
	conformancetest.CheckRestoreCancellation(t, definition, initial)
	for _, state := range []firstSuccessState{
		{Completed: true, Candidates: []run.ChildSpec{a}},
		{Candidates: []run.ChildSpec{a, a}},
		{Candidates: []run.ChildSpec{a, b}, Calls: []child.Invocation{{Request: new(turn(1).EffectID(0))}}},
		{Candidates: []run.ChildSpec{a, b}, Calls: []child.Invocation{{}, {}}},
		{Candidates: []run.ChildSpec{a, b}, Winner: new(uint32(0))},
	} {
		encoded := require(run.EncodeExecutionState(firstSuccessStateKind, state))
		if _, err := definition.Restore(t.Context(), encoded); !errors.Is(err, ErrInvalidExecutionState) {
			t.Fatalf("contradictory competition state admitted: %+v, %v", state, err)
		}
	}
	completed := require(run.EncodeExecutionState(firstSuccessStateKind, firstSuccessState{Completed: true}))
	done := require(definition.Restore(t.Context(), completed))
	if _, err := done.Step(t.Context(), turn(1)); !errors.Is(err, ErrInvalidProtocol) {
		t.Fatal("completed competition restarted", err)
	}
}

func TestCompetitionReturnsAllRejectionsAndPropagatesPredicateFailure(t *testing.T) {
	compiler := conformancetest.Compiler(t)
	candidate := require(Compile[time.Time, time.Time](compiler, Config{Name: "test.candidate", Description: "Wait.", Policy: Deadline{}}))
	input := require(run.EncodePayload([]run.ChildSpec{{Key: require(run.ParseChildKey("candidate")), ProgramRef: candidate.ProgramRef(), Input: require(run.EncodePayload(time.Now()))}}))
	cause := errors.New("cannot assess candidate")
	for _, sample := range []struct {
		revision string
		failure  error
	}{{"reject", nil}, {"fail", cause}} {
		program := require(Compile[[]run.ChildSpec, FirstSuccessResult](compiler, Config{Name: "test.competition", Description: "Assess one candidate.", Policy: FirstSuccess{MaxCandidates: 1, Revision: sample.revision, Accept: func(context.Context, run.ChildKey, run.Termination) (bool, error) { return false, sample.failure }}}))
		execution := require(program.Definition().Start(input))
		require(execution.Step(t.Context(), turn(1)))
		process := require(turn(1).Process.Child(require(run.ParseChildKey("candidate"))))
		transition, err := execution.Step(t.Context(), turn(2, run.Completion{Effect: turn(1).EffectID(0), Outcome: settled(process)}, run.Exit{Child: process, Termination: ended("finished")}))
		if sample.failure != nil {
			if !errors.Is(err, cause) || transition.Valid() {
				t.Fatal("predicate failure became candidate rejection", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		output, ok := transition.Output()
		result := require(output.Decode[FirstSuccessResult]())
		if !ok || !result.Valid() || result.Winner != nil || len(result.Candidates) != 1 {
			t.Fatal("unacceptable result became a winner")
		}
	}
	program := require(Compile[[]run.ChildSpec, FirstSuccessResult](compiler, Config{Name: "test.rejection", Description: "Retain spawn rejection.", Policy: FirstSuccess{MaxCandidates: 1, Revision: "accept", Accept: func(context.Context, run.ChildKey, run.Termination) (bool, error) {
		t.Fatal("rejected spawn reached predicate")
		return true, nil
	}}}))
	execution := require(program.Definition().Start(input))
	require(execution.Step(t.Context(), turn(1)))
	failure := require(run.NewFailure(run.FailureKindContract, "test.rejected", "spawn rejected"))
	outcome := require(run.NewFailedSettlement(failure, run.Payload{}))
	transition := require(execution.Step(t.Context(), turn(2, run.Completion{Effect: turn(1).EffectID(0), Outcome: outcome})))
	output, ok := transition.Output()
	result := require(output.Decode[FirstSuccessResult]())
	if !ok || !result.Valid() || result.Winner != nil || result.Candidates[0].Rejection == nil || result.Candidates[0].Rejection.Code() != failure.Code() {
		t.Fatal("spawn rejection lost its terminal evidence")
	}
}

func TestCompetitionRestorationBindsEachObligationToItsDeclaredCandidate(t *testing.T) {
	compiler := conformancetest.Compiler(t)
	program := require(Compile[[]run.ChildSpec, FirstSuccessResult](compiler, Config{Name: "test.competition", Description: "Bind candidate obligations.", Policy: FirstSuccess{MaxCandidates: 2, Revision: "accept", Accept: func(context.Context, run.ChildKey, run.Termination) (bool, error) { return true, nil }}}))
	a := run.ChildSpec{Key: require(run.ParseChildKey("a")), ProgramRef: program.ProgramRef(), Input: require(run.EncodePayload([]run.ChildSpec{}))}
	b := a
	b.Key = require(run.ParseChildKey("b"))
	processA := require(turn(1).Process.Child(a.Key))
	processB := require(turn(1).Process.Child(b.Key))
	foreign := require(turn(1).Process.Child(require(run.ParseChildKey("foreign"))))
	otherParent := require(run.ParseProcessID("other"))
	otherChild := require(otherParent.Child(b.Key))
	otherRequest := turn(1).EffectID(1)
	otherRequest.Process = otherParent
	termination := ended("winner")
	for _, sample := range []struct {
		name  string
		state firstSuccessState
	}{
		{"duplicate spawn identity", firstSuccessState{Calls: []child.Invocation{{Request: new(turn(1).EffectID(0))}, {Request: new(turn(1).EffectID(0))}}}},
		{"swapped spawn identity", firstSuccessState{Calls: []child.Invocation{{Request: new(turn(1).EffectID(1))}, {Request: new(turn(1).EffectID(0))}}}},
		{"different spawn turn", firstSuccessState{Calls: []child.Invocation{{Request: new(turn(1).EffectID(0))}, {Request: new(turn(2).EffectID(1))}}}},
		{"different spawn parent", firstSuccessState{Calls: []child.Invocation{{Request: new(turn(1).EffectID(0))}, {Request: &otherRequest}}}},
		{"duplicate child", firstSuccessState{Calls: []child.Invocation{{Process: &processA}, {Process: &processA}}}},
		{"undeclared child", firstSuccessState{Calls: []child.Invocation{{Process: &processA}, {Process: &foreign}}}},
		{"different child parent", firstSuccessState{Calls: []child.Invocation{{Process: &processA}, {Process: &otherChild}}}},
		{"winner before admission", firstSuccessState{Calls: []child.Invocation{{Termination: &termination}, {Request: new(turn(1).EffectID(1))}}, Winner: new(uint32(0))}},
		{"cancellation without winner", firstSuccessState{Calls: []child.Invocation{{Process: &processA}, {Process: &processB}}, Cancellations: []run.EffectID{turn(2).EffectID(0)}}},
		{"cancellation for different parent", firstSuccessState{Calls: []child.Invocation{{Termination: &termination}, {Process: &processB}}, Winner: new(uint32(0)), Cancellations: []run.EffectID{otherRequest}}},
		{"cancellation outside loser range", firstSuccessState{Calls: []child.Invocation{{Termination: &termination}, {Process: &processB}}, Winner: new(uint32(0)), Cancellations: []run.EffectID{turn(2).EffectID(1)}}},
		{"duplicate cancellation", firstSuccessState{Calls: []child.Invocation{{Termination: &termination}, {Process: &processB}}, Winner: new(uint32(0)), Cancellations: []run.EffectID{turn(2).EffectID(0), turn(2).EffectID(0)}}},
	} {
		t.Run(sample.name, func(t *testing.T) {
			sample.state.Candidates = []run.ChildSpec{a, b}
			state := require(run.EncodeExecutionState(firstSuccessStateKind, sample.state))
			if _, err := program.Definition().Restore(t.Context(), state); !errors.Is(err, ErrInvalidExecutionState) {
				t.Fatalf("restored obligations were not bound to candidate declarations: %v", err)
			}
		})
	}
}
