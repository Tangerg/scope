package workflow_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
)

func TestWorkflowRejectsUnrepresentableTypedContracts(t *testing.T) {
	child := mustDeployment(t, mustDefinition(t, "workflow.child",
		mustTransform(t, "identity", func(_ context.Context, input int) (int, error) { return input, nil }),
	), "typed-child")
	branches := []workflow.ForkBranch{{ID: "branch", Program: child}}
	for _, construct := range []func() (workflow.Stage, error){
		func() (workflow.Stage, error) {
			return workflow.Transform("bad_input", func(context.Context, chan int) (int, error) { return 0, nil })
		},
		func() (workflow.Stage, error) {
			return workflow.Transform("bad_output", func(context.Context, int) (chan int, error) { return nil, nil })
		},
		func() (workflow.Stage, error) {
			return workflow.Switch(workflow.SwitchConfig[chan int]{ID: "switch", Cases: []workflow.SwitchCase{{ID: "case", Program: child}}, Select: func(context.Context, chan int) (string, error) { return "case", nil }})
		},
		func() (workflow.Stage, error) {
			return workflow.Fork(workflow.ForkConfig[chan int, int, int]{ID: "fork", Branches: branches, WindowSize: 1, Reduce: func(context.Context, chan int, []int) (int, error) { return 0, nil }})
		},
		func() (workflow.Stage, error) {
			return workflow.Fork(workflow.ForkConfig[int, chan int, int]{ID: "fork", Branches: branches, WindowSize: 1, Reduce: func(context.Context, int, []chan int) (int, error) { return 0, nil }})
		},
		func() (workflow.Stage, error) {
			return workflow.Fork(workflow.ForkConfig[int, int, chan int]{ID: "fork", Branches: branches, WindowSize: 1, Reduce: func(context.Context, int, []int) (chan int, error) { return nil, nil }})
		},
		func() (workflow.Stage, error) {
			return workflow.Map(workflow.MapConfig[chan int, int]{ID: "map", Program: child, WindowSize: 1, MaxItems: 1})
		},
		func() (workflow.Stage, error) {
			return workflow.Map(workflow.MapConfig[int, chan int]{ID: "map", Program: child, WindowSize: 1, MaxItems: 1})
		},
		func() (workflow.Stage, error) {
			return workflow.Loop(workflow.LoopConfig[chan int]{ID: "loop", Body: child, Predicate: func(context.Context, chan int) (bool, error) { return false, nil }})
		},
	} {
		if stage, err := construct(); stage.Valid() || !errors.Is(err, workflow.ErrInvalidStage) {
			t.Fatalf("unrepresentable stage contract = %v, %v", stage, err)
		}
	}
}

func TestForkRequiresUnambiguousExactBranchBindings(t *testing.T) {
	child := mustDeployment(t, mustDefinition(t, "workflow.child",
		mustTransform(t, "identity", func(_ context.Context, input int) (int, error) { return input, nil }),
	), "branch-child")
	wrong := mustDeployment(t, mustDefinition(t, "workflow.wrong",
		mustTransform(t, "identity", func(_ context.Context, input string) (string, error) { return input, nil }),
	), "wrong-branch")
	for _, branches := range [][]workflow.ForkBranch{
		{{ID: "missing"}},
		{{ID: "invalid name", Program: child}},
		{{ID: "duplicate", Program: child}, {ID: "duplicate", Program: child}},
		{{ID: "wrong_contract", Program: wrong}},
	} {
		stage, err := workflow.Fork(workflow.ForkConfig[int, int, int]{
			ID: "fork", Branches: branches, WindowSize: 1,
			Reduce: func(context.Context, int, []int) (int, error) { return 0, nil },
		})
		if stage.Valid() || !errors.Is(err, workflow.ErrInvalidStage) {
			t.Fatalf("ambiguous or incompatible branch contract = %v, %v", stage, err)
		}
	}
}

func TestWorkflowRejectsUnconfiguredDefinitionsAndInputs(t *testing.T) {
	for _, definition := range []*workflow.Definition{nil, {}} {
		if execution, err := definition.Start(run.Payload{}); execution != nil || !errors.Is(err, workflow.ErrInvalidDefinitionConfig) {
			t.Fatalf("unconfigured Start = %v, %v", execution, err)
		}
		if execution, err := definition.Restore(t.Context(), run.ExecutionState{}); execution != nil || !errors.Is(err, workflow.ErrInvalidDefinitionConfig) {
			t.Fatalf("unconfigured Restore = %v, %v", execution, err)
		}
	}
	stage := mustTransform(t, "identity", func(_ context.Context, value int) (int, error) { return value, nil })
	if definition, err := workflow.NewDefinition(workflow.DefinitionConfig{Name: "invalid name", Description: "Run a workflow.", Stages: []workflow.Stage{stage}}); definition != nil || !errors.Is(err, workflow.ErrInvalidDefinitionConfig) {
		t.Fatalf("invalid definition identity = %v, %v", definition, err)
	}
	input, err := run.EncodePayload("wrong input type")
	if err != nil {
		t.Fatal(err)
	}
	if execution, err := mustDefinition(t, "workflow.input", stage).Start(input); execution != nil || !errors.Is(err, run.ErrInvalidPayload) {
		t.Fatalf("input outside the first stage contract = %v, %v", execution, err)
	}
}

func TestMapCompilesWithOnlyItsChildIdentity(t *testing.T) {
	child := mustDeployment(t, mustDefinition(t, "workflow.child",
		mustTransform(t, "identity", func(_ context.Context, input int) (int, error) { return input, nil }),
	), "typed-child")
	stage, err := workflow.Map(workflow.MapConfig[int, int]{ID: "items", Program: child, WindowSize: 1, MaxItems: 4})
	if err != nil {
		t.Fatal(err)
	}
	cfg := workflow.Config{Name: "workflow.map", Description: "Map a bound program.", Stages: []workflow.Stage{stage}, BuildIdentity: run.ComputeDigest([]byte("workflow build"))}
	program, err := workflow.Compile[[]int, []int](t.Context(), cfg)
	if err != nil || !program.Valid() {
		t.Fatalf("a declarative map requires no callback identity: %v", err)
	}
	cfg.CallbackIdentity = run.ComputeDigest([]byte("unbound callback"))
	if _, err := workflow.Compile[[]int, []int](t.Context(), cfg); !errors.Is(err, run.ErrInvalidProgram) {
		t.Fatalf("accepted an identity for a callback that does not exist: %v", err)
	}
}
