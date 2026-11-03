package workflow

import (
	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run/runtest"

	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/run"
)

type loopValue struct {
	Value int `json:"value"`
}

func TestLoopRunsAtLeastOnceAndReportsSatisfiedOrExhausted(t *testing.T) {
	body := mustProgram(t, mustDefinition(t, "test.loop_body",
		mustTransform(t, "increment", func(_ context.Context, input loopValue) (loopValue, error) {
			return loopValue{Value: input.Value + 1}, nil
		}),
	), "loop-body")
	for _, test := range []struct {
		name           string
		initial        int
		maximum        run.Quota
		threshold      int
		wantValue      int
		wantIterations uint32
		wantSatisfied  bool
	}{
		{name: "unlimited", threshold: 4, wantValue: 4, wantIterations: 4, wantSatisfied: true},
		{name: "satisfied", initial: 0, maximum: run.NewQuota(5), threshold: 3, wantValue: 3, wantIterations: 3, wantSatisfied: true},
		{name: "exhausted", initial: 0, maximum: run.NewQuota(2), threshold: 3, wantValue: 2, wantIterations: 2, wantSatisfied: false},
		{name: "at_least_once", initial: 3, maximum: run.NewQuota(5), threshold: 3, wantValue: 4, wantIterations: 1, wantSatisfied: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			stage, err := Loop(LoopConfig[loopValue]{
				ID: "improve", Body: run.ChildBinding{Program: body, Budget: mustBudget(t)}, MaxIterations: test.maximum,
				Predicate: func(_ context.Context, value loopValue) (bool, error) { return value.Value >= test.threshold, nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			if !stage.Valid() {
				t.Fatalf("Loop Stage = %#v", stage)
			}
			program := mustProgram(t, mustDefinition(t, "test.loop_"+test.name, stage), "loop-"+test.name)
			store := run.NewMemoryStore()
			runtime, err := run.New(t.Context(), run.Config{Build: conformancetest.Build, Store: store})
			if err != nil {
				t.Fatal(err)
			}
			input, _ := run.EncodePayload(loopValue{Value: test.initial})
			result, err := runtest.Observe(context.Background(), runtime, program, input)
			if err != nil {
				t.Fatal(err)
			}
			output := decodeCompleted[LoopResult[loopValue]](t, result)
			if !output.Valid() || output.Value.Value != test.wantValue ||
				output.Iterations != uint64(test.wantIterations) || output.Satisfied != test.wantSatisfied {
				t.Fatalf("Loop output = %#v", output)
			}
			tree, _, err := store.Load(t.Context(), result.ProcessID())
			if err != nil {
				t.Fatal(err)
			}
			got := 0
			for _, entry := range tree.Entries {
				if _, ok := entry.Record.(run.Started); ok {
					got++
				}
			}
			if got != int(test.wantIterations)+1 {
				t.Fatalf("Loop tree Process count = %d, want %d", got, test.wantIterations+1)
			}
			if err := runtime.Close(context.WithoutCancel(t.Context())); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLoopPropagatesBodyFailure(t *testing.T) {
	body := mustProgram(t, mustDefinition(t, "test.failing_loop_body",
		mustTransform(t, "fail", func(context.Context, loopValue) (loopValue, error) {
			return loopValue{}, errors.New("deliberate Loop body failure")
		}),
	), "failing-loop-body")
	stage, err := Loop(LoopConfig[loopValue]{
		ID: "improve", Body: run.ChildBinding{Program: body, Budget: mustBudget(t)}, MaxIterations: run.NewQuota(2),
		Predicate: func(context.Context, loopValue) (bool, error) { return false, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	program := mustProgram(t, mustDefinition(t, "test.failing_loop", stage), "failing-loop")
	runtime, _ := run.New(t.Context(), run.Config{Build: conformancetest.Build, Store: run.NewMemoryStore()})
	input, _ := run.EncodePayload(loopValue{})
	result, err := runtest.Observe(context.Background(), runtime, program, input)
	if err != nil {
		t.Fatal(err)
	}
	failure, present := result.Termination().Failure()
	if result.Termination().Status() != run.StatusFailed || !present || failure.Kind() != run.FailureKindExecution ||
		failure.Code() != "execution.step.failed" || failure.Message() != `transform "fail": deliberate Loop body failure` {
		t.Fatalf("Loop termination = %#v", result.Termination())
	}
	if err := runtime.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestLoopRequiresPredicateAndExactBodyContract(t *testing.T) {
	validBody := mustProgram(t, mustDefinition(t, "test.valid_loop_body",
		mustTransform(t, "identity", func(_ context.Context, input loopValue) (loopValue, error) { return input, nil }),
	), "valid-loop-body")
	wrongBody := mustProgram(t, mustDefinition(t, "test.wrong_loop_body",
		mustTransform(t, "wrong", func(context.Context, textValue) (textValue, error) { return textValue{}, nil }),
	), "wrong-loop-body")
	valid := LoopConfig[loopValue]{
		ID: "improve", Body: run.ChildBinding{Program: validBody, Budget: mustBudget(t)}, MaxIterations: run.NewQuota(2),
		Predicate: func(context.Context, loopValue) (bool, error) { return true, nil },
	}
	for name, config := range map[string]LoopConfig[loopValue]{
		"zero finite maximum": {MaxIterations: run.NewQuota(0), ID: valid.ID, Body: valid.Body, Predicate: valid.Predicate},
		"nil predicate": {
			ID: valid.ID, Body: valid.Body, MaxIterations: valid.MaxIterations,
		},
		"body mismatch": {
			ID: valid.ID, Body: run.ChildBinding{Program: wrongBody, Budget: valid.Body.Budget},
			MaxIterations: valid.MaxIterations, Predicate: valid.Predicate,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Loop(config); !errors.Is(err, ErrInvalidStage) {
				t.Fatalf("Loop error = %v", err)
			}
		})
	}
	if (LoopResult[loopValue]{}).Valid() {
		t.Fatal("zero LoopResult is valid")
	}
}
