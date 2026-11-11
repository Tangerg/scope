package coordination

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/run/runtest"
	"github.com/Tangerg/scope/agent/workflow"
	"github.com/Tangerg/scope/core/jsonschema"
)

func require[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func turn(step uint64, messages ...run.Message) run.Turn {
	return run.Turn{Process: require(run.ParseProcessID("coordination-test")), Step: step, Messages: messages}
}

func settled(value any) run.Settlement {
	return require(run.NewSettlement(run.SettlementStatusSucceeded, require(run.EncodePayload(value))))
}

func ended(value any) run.Termination {
	wire := require(jsonv2.Marshal(struct {
		Output run.Payload `json:"output"`
	}{require(run.EncodePayload(value))}))
	var result run.Termination
	if err := jsonv2.Unmarshal(wire, &result); err != nil {
		panic(err)
	}
	return result
}

func waitFor[O any](t *testing.T, handle *run.Handle[O]) run.Wait {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		view, err := handle.Inspect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		process, _ := view.Process(handle.ID())
		if len(process.Waits) == 1 {
			return process.Waits[0]
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return run.Wait{}
		case <-ticker.C:
		}
	}
}

func TestInputGateDeclaresLocalWaitWithoutHandshake(t *testing.T) {
	runtime := conformancetest.Runtime(t, run.Config{})
	schema := require(jsonschema.For[string]())
	program := require(Compile[string, string](runtime.Compiler(), Config{Name: "test.input", Description: "Ask one question.", Policy: InputGate{RequestSchema: schema, AnswerSchema: schema, MaxAnswerBytes: 16}}))
	input := require(run.EncodePayload("question"))
	definition := program.Definition()
	execution := require(definition.Start(input))
	transition := require(execution.Step(t.Context(), turn(1)))
	if transition.Kind() != run.TransitionKindYield || len(transition.Effects()) != 0 || len(transition.Waits()) != 1 {
		t.Fatalf("wait requires extra lifecycle: %+v", transition)
	}
	state := require(execution.Snapshot())
	key := transition.Waits()[0].Key
	signal := run.Signal{ID: require(run.ParseSignalID("answer")), Wait: key, Payload: require(run.EncodePayload("yes"))}
	runtest.RunDefinitionConformance(t, runtest.DefinitionConformanceConfig{Definition: definition, Input: input, InitialTurn: turn(1), FollowingTurns: []run.Turn{turn(2, signal)}, RestoredCases: []runtest.ExecutionConformanceCase{{Name: "local wait", State: state, Turn: turn(2, signal)}}})
}

func TestInputGateAnswerIsSchemaCheckedAndStableAcrossRecovery(t *testing.T) {
	store := run.NewMemoryStore()
	runtime := conformancetest.Runtime(t, run.Config{Store: store, CheckpointInterval: 1})
	schema := require(jsonschema.For[string]())
	config := Config{Name: "test.input", Description: "Ask one question.", Policy: InputGate{RequestSchema: schema, AnswerSchema: schema, MaxAnswerBytes: 16}}
	program := require(Compile[string, string](runtime.Compiler(), config))
	handle := require(runtime.Start(t.Context(), program, run.StartRequest[string]{ID: run.StartID{Scope: "test", Key: t.Name()}, Input: "question"}))
	wait := waitFor(t, handle)
	if err := handle.Answer(t.Context(), wait.Key, 42); !errors.Is(err, run.ErrSignalRejected) {
		t.Fatalf("wrong answer admitted: %v", err)
	}
	if err := runtime.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	recoveredRuntime := conformancetest.Runtime(t, run.Config{Store: store})
	recoveredProgram := require(Compile[string, string](recoveredRuntime.Compiler(), config))
	recovered := require(recoveredRuntime.Recover(t.Context(), recoveredProgram, handle.ID()))
	if restored := waitFor(t, recovered); restored.Key != wait.Key {
		t.Fatal("recovery changed question identity")
	}
	if err := recovered.Answer(t.Context(), wait.Key, "yes"); err != nil {
		t.Fatal(err)
	}
	result := require(recovered.Await(t.Context()))
	answer := require(result.Output())
	if answer != "yes" {
		t.Fatalf("answer=%+v", answer)
	}
}

type resolver map[run.ProgramRef]run.Program[run.Payload, run.Payload]

func (r resolver) Resolve(reference run.ProgramRef) (run.Program[run.Payload, run.Payload], error) {
	program, found := r[reference]
	if !found {
		return run.Program[run.Payload, run.Payload]{}, run.ErrInvalidProgram
	}
	return program, nil
}

func TestFirstSuccessCancelsAndDrainsLosingSubtrees(t *testing.T) {
	store := run.NewMemoryStore()
	programs := resolver{}
	runtime := conformancetest.Runtime(t, run.Config{Store: store, ProgramResolver: programs})
	stage := require(workflow.Transform("identity", func(_ context.Context, input string) (string, error) { return input, nil }))
	winner := require(workflow.Compile[string, string](runtime.Compiler(), workflow.Config{Name: "test.winner", Description: "Return immediately.", Revision: "identity", Stages: []workflow.Stage{stage}}))
	loser := require(Compile[time.Time, time.Time](runtime.Compiler(), Config{Name: "test.loser", Description: "Wait for later.", Policy: Deadline{}}))
	programs[winner.ProgramRef()] = winner.Erase()
	programs[loser.ProgramRef()] = loser.Erase()
	program := require(Compile[[]run.ChildSpec, FirstSuccessResult](runtime.Compiler(), Config{Name: "test.competition", Description: "Choose the first completed candidate.", Policy: FirstSuccess{MaxCandidates: 2, Revision: "completed", Accept: func(context.Context, run.ChildKey, run.Termination) (bool, error) { return true, nil }}}))
	candidates := []run.ChildSpec{{Key: require(run.ParseChildKey("winner")), ProgramRef: winner.ProgramRef(), Input: require(run.EncodePayload("yes"))}, {Key: require(run.ParseChildKey("loser")), ProgramRef: loser.ProgramRef(), Input: require(run.EncodePayload(time.Now().Add(time.Hour)))}}
	handle := require(runtime.Start(t.Context(), program, run.StartRequest[[]run.ChildSpec]{ID: run.StartID{Scope: "test", Key: t.Name()}, Input: candidates}))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result := require(handle.Await(ctx))
	output := require(result.Output())
	if !output.Valid() || output.Winner == nil || *output.Winner != 0 || output.Candidates[1].Outcome.Status() != run.StatusCanceled {
		t.Fatalf("result=%+v", output)
	}
	log, _, err := store.Load(ctx, handle.ID())
	if err != nil {
		t.Fatal(err)
	}
	inspection := require(log.Inspect())
	if len(inspection.Processes) != 1 {
		t.Fatal("competition returned before children reaped")
	}
	exits := 0
	for _, entry := range log.Entries {
		if exited, ok := entry.Record.(run.Exited); ok {
			if exited.Process == handle.ID() && exits != 2 {
				t.Fatal("root exit preceded subtree drain")
			}
			exits++
		}
	}
}

func TestFirstSuccessMatchesExplicitCompletionAndExit(t *testing.T) {
	runtime := conformancetest.Runtime(t, run.Config{})
	deadline := require(Compile[time.Time, time.Time](runtime.Compiler(), Config{Name: "test.deadline", Description: "Wait.", Policy: Deadline{}}))
	definition := require(newFirstSuccess("test.competition", "Choose an acceptable result.", FirstSuccess{MaxCandidates: 2, Revision: "positive", Accept: func(_ context.Context, _ run.ChildKey, result run.Termination) (bool, error) {
		output, _ := result.Output()
		value, err := output.Decode[int]()
		return value > 0, err
	}}))
	input := require(run.EncodePayload([]run.ChildSpec{{Key: require(run.ParseChildKey("a")), ProgramRef: deadline.ProgramRef(), Input: require(run.EncodePayload(time.Now()))}, {Key: require(run.ParseChildKey("b")), ProgramRef: deadline.ProgramRef(), Input: require(run.EncodePayload(time.Now()))}}))
	execution := require(definition.Start(input))
	first := require(execution.Step(t.Context(), turn(1)))
	if len(first.Effects()) != 2 {
		t.Fatal(first)
	}
	a := require(turn(1).Process.Child(require(run.ParseChildKey("a"))))
	b := require(turn(1).Process.Child(require(run.ParseChildKey("b"))))
	second := turn(2, run.Completion{Effect: turn(1).EffectID(1), Outcome: settled(b)}, run.Completion{Effect: turn(1).EffectID(0), Outcome: settled(a)}, run.Exit{Child: b, Termination: ended(2)})
	transition := require(execution.Step(t.Context(), second))
	if transition.Kind() != run.TransitionKindYield || len(transition.Effects()) != 1 {
		t.Fatal("loser was not canceled")
	}
	state := require(execution.Snapshot())
	third := turn(3, run.Exit{Child: a, Termination: ended(-1)}, run.Completion{Effect: second.EffectID(0), Outcome: settled(struct{}{})})
	runtest.RunDefinitionConformance(t, runtest.DefinitionConformanceConfig{Definition: definition, Input: input, InitialTurn: turn(1), FollowingTurns: []run.Turn{second, third}, RestoredCases: []runtest.ExecutionConformanceCase{{Name: "cancel drain", State: state, Turn: third}}})
}

func TestDeadlineUsesSameIdentityTimerAndCompletesAtAbsoluteTime(t *testing.T) {
	runtime := conformancetest.Runtime(t, run.Config{})
	program := require(Compile[time.Time, time.Time](runtime.Compiler(), Config{Name: "test.deadline", Description: "Wait for the instant.", Policy: Deadline{}}))
	deadline := time.Now().Add(-time.Second).UTC()
	output := require(runtime.Run(t.Context(), program, deadline))
	if !output.Equal(deadline) {
		t.Fatal("deadline output changed")
	}
	effect := require(newTimerEffect(deadline))
	if policy := (deadlineDispatcher{}).Policy(effect); policy.Replay != run.ReplayPolicySameIdentity {
		t.Fatal("side-effect-free timer cannot recover")
	}
}

func TestCoordinationRejectsMalformedPolicyAndState(t *testing.T) {
	compiler := conformancetest.Compiler(t)
	for _, policy := range []Policy{nil, InputGate{}, FirstSuccess{}, FirstSuccess{MaxCandidates: 1, Accept: func(context.Context, run.ChildKey, run.Termination) (bool, error) { return true, nil }}} {
		if _, err := Compile[run.Payload, run.Payload](compiler, Config{Name: "test.invalid", Description: "Reject invalid configuration.", Policy: policy}); err == nil {
			t.Fatalf("accepted policy %#v", policy)
		}
	}
	definition := require(newInputGate("test.input", "Ask.", InputGate{RequestSchema: require(jsonschema.For[string]()), AnswerSchema: require(jsonschema.For[string]())}))
	for _, wire := range []string{`{"phase":"waiting"}`, `{"phase":"completed","request":"duplicate"}`, `{"phase":"waiting","request":"question","unexpected":1}`} {
		state := require(run.NewExecutionState(inputGateStateKind, require(run.ParsePayload([]byte(wire)))))
		if _, err := definition.Restore(t.Context(), state); err == nil {
			t.Fatalf("accepted %s", wire)
		}
	}
}
