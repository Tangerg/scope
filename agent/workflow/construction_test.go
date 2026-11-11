package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
)

func TestWorkflowRejectsUnrepresentableTypedContracts(t *testing.T) {
	child := mustProgram(t, mustDefinition(t, "child",
		mustTransform(t, "identity", func(_ context.Context, input int) (int, error) { return input, nil }),
	), "typed-child")
	branches := []ForkBranch{{ID: "branch", Child: run.ChildBinding{Program: child}}}
	for _, construct := range []func() (Stage, error){
		func() (Stage, error) {
			return Transform("bad_input", func(context.Context, chan int) (int, error) { return 0, nil })
		},
		func() (Stage, error) {
			return Transform("bad_output", func(context.Context, int) (chan int, error) { return nil, nil })
		},
		func() (Stage, error) {
			return Switch(SwitchConfig[chan int]{ID: "switch", Cases: []SwitchCase{{ID: "case", Child: run.ChildBinding{Program: child}}}, Select: func(context.Context, chan int) (string, error) { return "case", nil }})
		},
		func() (Stage, error) {
			return Fork(ForkConfig[chan int, int, int]{ID: "fork", Branches: branches, WindowSize: 1, Reduce: func(context.Context, chan int, []int) (int, error) { return 0, nil }})
		},
		func() (Stage, error) {
			return Fork(ForkConfig[int, chan int, int]{ID: "fork", Branches: branches, WindowSize: 1, Reduce: func(context.Context, int, []chan int) (int, error) { return 0, nil }})
		},
		func() (Stage, error) {
			return Fork(ForkConfig[int, int, chan int]{ID: "fork", Branches: branches, WindowSize: 1, Reduce: func(context.Context, int, []int) (chan int, error) { return nil, nil }})
		},
		func() (Stage, error) {
			return Map(MapConfig[chan int, int]{ID: "map", Child: run.ChildBinding{Program: child}, WindowSize: 1, MaxItems: 1})
		},
		func() (Stage, error) {
			return Map(MapConfig[int, chan int]{ID: "map", Child: run.ChildBinding{Program: child}, WindowSize: 1, MaxItems: 1})
		},
		func() (Stage, error) {
			return Loop(LoopConfig[chan int]{ID: "loop", Body: run.ChildBinding{Program: child}, Predicate: func(context.Context, chan int) (bool, error) { return false, nil }})
		},
	} {
		if stage, err := construct(); stage.Valid() || !errors.Is(err, ErrInvalidStage) {
			t.Fatalf("unrepresentable stage contract = %v, %v", stage, err)
		}
	}
}

func TestForkRequiresUnambiguousExactBranchBindings(t *testing.T) {
	child := mustProgram(t, mustDefinition(t, "child",
		mustTransform(t, "identity", func(_ context.Context, input int) (int, error) { return input, nil }),
	), "branch-child")
	wrong := mustProgram(t, mustDefinition(t, "wrong",
		mustTransform(t, "identity", func(_ context.Context, input string) (string, error) { return input, nil }),
	), "wrong-branch")
	for _, branches := range [][]ForkBranch{
		{{ID: "missing"}},
		{{ID: "invalid name", Child: run.ChildBinding{Program: child}}},
		{{ID: "duplicate", Child: run.ChildBinding{Program: child}}, {ID: "duplicate", Child: run.ChildBinding{Program: child}}},
		{{ID: "wrong_contract", Child: run.ChildBinding{Program: wrong}}},
	} {
		stage, err := Fork(ForkConfig[int, int, int]{
			ID: "fork", Branches: branches, WindowSize: 1,
			Reduce: func(context.Context, int, []int) (int, error) { return 0, nil },
		})
		if stage.Valid() || !errors.Is(err, ErrInvalidStage) {
			t.Fatalf("ambiguous or incompatible branch contract = %v, %v", stage, err)
		}
	}
}

func TestWorkflowRejectsInvalidDefinitionsAndInputs(t *testing.T) {
	stage := mustTransform(t, "identity", func(_ context.Context, value int) (int, error) { return value, nil })
	if definition, err := newDefinition(Config{Name: "invalid name", Description: "Run a ", Stages: []Stage{stage}}); definition != nil || !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("invalid definition identity = %v, %v", definition, err)
	}
	input, err := run.EncodePayload("wrong input type")
	if err != nil {
		t.Fatal(err)
	}
	if execution, err := mustDefinition(t, "input", stage).Start(input); execution != nil || !errors.Is(err, run.ErrInvalidPayload) {
		t.Fatalf("input outside the first stage contract = %v, %v", execution, err)
	}
}

func TestMapCompilesWithOnlyItsChildIdentity(t *testing.T) {
	child := mustProgram(t, mustDefinition(t, "child",
		mustTransform(t, "identity", func(_ context.Context, input int) (int, error) { return input, nil }),
	), "typed-child")
	stage, err := Map(MapConfig[int, int]{ID: "items", Child: run.ChildBinding{Program: child}, WindowSize: 1, MaxItems: 4})
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Name: "map", Description: "Map a bound program.", Stages: []Stage{stage}}
	program, err := Compile[[]int, []int](conformancetest.Compiler(t), cfg)
	if err != nil || !program.Valid() {
		t.Fatalf("a declarative map requires no callback identity: %v", err)
	}
	cfg.Revision = "unbound callback"
	if _, err := Compile[[]int, []int](conformancetest.Compiler(t), cfg); !errors.Is(err, run.ErrInvalidProgram) {
		t.Fatalf("accepted an identity for a callback that does not exist: %v", err)
	}
}
