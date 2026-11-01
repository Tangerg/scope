package workflow_test

import (
	"github.com/Tangerg/scope/agent/run/runtest"

	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
)

type loopValue struct {
	Value int `json:"value"`
}

func TestLoopRunsAtLeastOnceAndReportsSatisfiedOrExhausted(t *testing.T) {
	body := mustDeployment(t, mustDefinition(t, "test.workflow.loop_body",
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
			stage, err := workflow.Loop(workflow.LoopConfig[loopValue]{
				ID: "improve", Body: body, Budget: mustBudget(t), MaxIterations: test.maximum,
				Predicate: func(_ context.Context, value loopValue) (bool, error) { return value.Value >= test.threshold, nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			if !stage.Valid() {
				t.Fatalf("Loop Stage = %#v", stage)
			}
			deployment := mustDeployment(t, mustDefinition(t, "test.workflow.loop_"+test.name, stage), "loop-"+test.name)
			engine, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
			if err != nil {
				t.Fatal(err)
			}
			input, _ := run.EncodePayload(loopValue{Value: test.initial})
			result, err := runtest.Observe(context.Background(), engine, deployment, input)
			if err != nil {
				t.Fatal(err)
			}
			output := decodeCompleted[workflow.LoopResult[loopValue]](t, result)
			if !output.Valid() || output.Value.Value != test.wantValue ||
				output.Iterations != uint64(test.wantIterations) || output.Satisfied != test.wantSatisfied {
				t.Fatalf("Loop output = %#v", output)
			}
			tree, err := engine.CaptureTree(context.Background(), result.ProcessID())
			if err != nil {
				t.Fatal(err)
			}
			if got := len(tree.ProcessSnapshots()); got != int(test.wantIterations)+1 {
				t.Fatalf("Loop tree Process count = %d, want %d", got, test.wantIterations+1)
			}
			if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLoopPropagatesBodyFailure(t *testing.T) {
	body := mustDeployment(t, mustDefinition(t, "test.workflow.failing_loop_body",
		mustTransform(t, "fail", func(context.Context, loopValue) (loopValue, error) {
			return loopValue{}, errors.New("deliberate Loop body failure")
		}),
	), "failing-loop-body")
	stage, err := workflow.Loop(workflow.LoopConfig[loopValue]{
		ID: "improve", Body: body, Budget: mustBudget(t), MaxIterations: run.NewQuota(2),
		Predicate: func(context.Context, loopValue) (bool, error) { return false, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment := mustDeployment(t, mustDefinition(t, "test.workflow.failing_loop", stage), "failing-loop")
	engine, _ := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
	input, _ := run.EncodePayload(loopValue{})
	result, err := runtest.Observe(context.Background(), engine, deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	failure, present := result.Termination().Failure()
	if result.Termination().Status() != run.StatusFailed || !present || failure.Kind() != run.FailureKindExecution ||
		failure.Code() != "execution.step.failed" || failure.Message() != `transform "fail": deliberate Loop body failure` {
		t.Fatalf("Loop termination = %#v", result.Termination())
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestLoopRequiresPredicateAndExactBodyContract(t *testing.T) {
	validBody := mustDeployment(t, mustDefinition(t, "test.workflow.valid_loop_body",
		mustTransform(t, "identity", func(_ context.Context, input loopValue) (loopValue, error) { return input, nil }),
	), "valid-loop-body")
	wrongBody := mustDeployment(t, mustDefinition(t, "test.workflow.wrong_loop_body",
		mustTransform(t, "wrong", func(context.Context, textValue) (textValue, error) { return textValue{}, nil }),
	), "wrong-loop-body")
	valid := workflow.LoopConfig[loopValue]{
		ID: "improve", Body: validBody, Budget: mustBudget(t), MaxIterations: run.NewQuota(2),
		Predicate: func(context.Context, loopValue) (bool, error) { return true, nil },
	}
	for name, config := range map[string]workflow.LoopConfig[loopValue]{
		"zero finite maximum": {MaxIterations: run.NewQuota(0), ID: valid.ID, Body: valid.Body, Budget: valid.Budget, Predicate: valid.Predicate},
		"nil predicate": {
			ID: valid.ID, Body: valid.Body, Budget: valid.Budget, MaxIterations: valid.MaxIterations,
		},
		"body mismatch": {
			ID: valid.ID, Body: wrongBody, Budget: valid.Budget,
			MaxIterations: valid.MaxIterations, Predicate: valid.Predicate,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := workflow.Loop(config); !errors.Is(err, workflow.ErrInvalidStage) {
				t.Fatalf("Loop error = %v", err)
			}
		})
	}
	if (workflow.LoopResult[loopValue]{}).Valid() {
		t.Fatal("zero LoopResult is valid")
	}
}
