package workflow

import (
	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run/runtest"

	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Tangerg/scope/agent/run"

	"github.com/Tangerg/scope/core/jsonschema"
)

var (
	errDecodeValue = errors.New("codec refused to decode the value")
	errEncodeValue = errors.New("codec refused to encode the value")
)

type refusingDecode int

func (*refusingDecode) UnmarshalJSON([]byte) error { return errDecodeValue }
func (refusingDecode) JSONSchemaModel() any        { return int(0) }

type refusingEncode int

func (refusingEncode) MarshalJSON() ([]byte, error) { return nil, errEncodeValue }
func (refusingEncode) JSONSchemaModel() any         { return int(0) }

type wrongShape int

func (wrongShape) MarshalJSON() ([]byte, error) { return []byte(`"not an integer"`), nil }
func (wrongShape) JSONSchemaModel() any         { return int(0) }

func TestTypedCodecFailuresCannotPublishWorkflowOutput(t *testing.T) {
	child := mustProgram(t, mustDefinition(t, "codec_child",
		mustTransform(t, "identity", func(_ context.Context, input int) (int, error) { return input, nil }),
	), "codec-child")
	branches := []ForkBranch{{ID: "child", Child: run.ChildBinding{Program: child, Budget: mustBudget(t)}}}
	for _, test := range []struct {
		name       string
		input      any
		stage      func() (Stage, error)
		diagnostic string
	}{
		{"transform_input", 3, func() (Stage, error) {
			return Transform("work", func(context.Context, refusingDecode) (int, error) { return 0, nil })
		}, errDecodeValue.Error()},
		{"transform_output", 3, func() (Stage, error) {
			return Transform("work", func(context.Context, int) (refusingEncode, error) { return 0, nil })
		}, errEncodeValue.Error()},
		{"transform_output_schema", 3, func() (Stage, error) {
			return Transform("work", func(context.Context, int) (wrongShape, error) { return 0, nil })
		}, "output contract"},
		{"switch_input", 3, func() (Stage, error) {
			return Switch(SwitchConfig[refusingDecode]{ID: "work",
				Cases:  []SwitchCase{{ID: "child", Child: run.ChildBinding{Program: codecProgram[refusingDecode, int](t), Budget: mustBudget(t)}}},
				Select: func(context.Context, refusingDecode) (string, error) { return "child", nil },
			})
		}, errDecodeValue.Error()},
		{"fork_input", 3, func() (Stage, error) {
			return Fork(ForkConfig[refusingDecode, int, int]{ID: "work", Branches: codecBranches[refusingDecode, int](t), WindowSize: 1,
				Reduce: func(context.Context, refusingDecode, []int) (int, error) { return 0, nil },
			})
		}, errDecodeValue.Error()},
		{"fork_branch", 3, func() (Stage, error) {
			return Fork(ForkConfig[int, refusingDecode, int]{ID: "work", Branches: codecBranches[int, refusingDecode](t), WindowSize: 1,
				Reduce: func(context.Context, int, []refusingDecode) (int, error) { return 0, nil },
			})
		}, errDecodeValue.Error()},
		{"fork_output", 3, func() (Stage, error) {
			return Fork(ForkConfig[int, int, refusingEncode]{ID: "work", Branches: branches, WindowSize: 1,
				Reduce: func(context.Context, int, []int) (refusingEncode, error) { return 0, nil },
			})
		}, errEncodeValue.Error()},
		{"fork_output_schema", 3, func() (Stage, error) {
			return Fork(ForkConfig[int, int, wrongShape]{ID: "work", Branches: branches, WindowSize: 1,
				Reduce: func(context.Context, int, []int) (wrongShape, error) { return 0, nil },
			})
		}, "result contract"},
		{"map_input_decode", []int{3}, func() (Stage, error) {
			return Map(MapConfig[refusingDecode, int]{ID: "work", Child: run.ChildBinding{Program: codecProgram[refusingDecode, int](t), Budget: mustBudget(t)}, WindowSize: 1, MaxItems: 1})
		}, errDecodeValue.Error()},
		{"map_input_encode", []int{3}, func() (Stage, error) {
			return Map(MapConfig[refusingEncode, int]{ID: "work", Child: run.ChildBinding{Program: codecProgram[refusingEncode, int](t), Budget: mustBudget(t)}, WindowSize: 1, MaxItems: 1})
		}, errEncodeValue.Error()},
		{"map_input_schema", []int{3}, func() (Stage, error) {
			return Map(MapConfig[wrongShape, int]{ID: "work", Child: run.ChildBinding{Program: codecProgram[wrongShape, int](t), Budget: mustBudget(t)}, WindowSize: 1, MaxItems: 1})
		}, "integer"},
		{"map_output_decode", []int{3}, func() (Stage, error) {
			return Map(MapConfig[int, refusingDecode]{ID: "work", Child: run.ChildBinding{Program: codecProgram[int, refusingDecode](t), Budget: mustBudget(t)}, WindowSize: 1, MaxItems: 1})
		}, errDecodeValue.Error()},
		{"map_output_encode", []int{3}, func() (Stage, error) {
			return Map(MapConfig[int, refusingEncode]{ID: "work", Child: run.ChildBinding{Program: codecProgram[int, refusingEncode](t), Budget: mustBudget(t)}, WindowSize: 1, MaxItems: 1})
		}, errEncodeValue.Error()},
		{"map_output_schema", []int{3}, func() (Stage, error) {
			return Map(MapConfig[int, wrongShape]{ID: "work", Child: run.ChildBinding{Program: codecProgram[int, wrongShape](t), Budget: mustBudget(t)}, WindowSize: 1, MaxItems: 1})
		}, "result contract"},
		{"loop_decode", 3, func() (Stage, error) {
			return Loop(LoopConfig[refusingDecode]{ID: "work", Body: run.ChildBinding{Program: codecProgram[refusingDecode, refusingDecode](t), Budget: mustBudget(t)}, MaxIterations: run.NewQuota(1),
				Predicate: func(context.Context, refusingDecode) (bool, error) { return true, nil },
			})
		}, errDecodeValue.Error()},
		{"loop_encode", 3, func() (Stage, error) {
			return Loop(LoopConfig[refusingEncode]{ID: "work", Body: run.ChildBinding{Program: codecProgram[refusingEncode, refusingEncode](t), Budget: mustBudget(t)}, MaxIterations: run.NewQuota(1),
				Predicate: func(context.Context, refusingEncode) (bool, error) { return true, nil },
			})
		}, errEncodeValue.Error()},
		{"loop_output_schema", 3, func() (Stage, error) {
			return Loop(LoopConfig[wrongShape]{ID: "work", Body: run.ChildBinding{Program: codecProgram[wrongShape, wrongShape](t), Budget: mustBudget(t)}, MaxIterations: run.NewQuota(1),
				Predicate: func(context.Context, wrongShape) (bool, error) { return true, nil },
			})
		}, "integer"},
	} {
		t.Run(test.name, func(t *testing.T) {
			stage, err := test.stage()
			if err != nil {
				t.Fatal(err)
			}
			program := mustProgram(t, mustDefinition(t, "codec_"+test.name, stage), test.name)
			store := run.NewMemoryStore()
			runtime, err := run.New(t.Context(), run.Config{Build: conformancetest.Build, Store: store})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if closeErr := runtime.Close(context.WithoutCancel(t.Context())); closeErr != nil {
					t.Error(closeErr)
				}
			})
			input, err := run.EncodePayload(test.input)
			if err != nil {
				t.Fatal(err)
			}
			result, err := runtest.Observe(t.Context(), runtime, program, input)
			if err != nil {
				t.Fatal(err)
			}
			failure, failed := result.Termination().Failure()
			if !failed || result.Termination().Status() != run.StatusFailed || !strings.Contains(failure.Message(), test.diagnostic) {
				t.Fatalf("codec failure was not preserved: %s, %v", result.Termination().Status(), failure)
			}
			if _, completed := result.Termination().Output(); completed {
				t.Fatal("invalid typed value escaped as a completed output")
			}
			head, found, err := store.Load(t.Context(), result.ProcessID())
			if err != nil || !found {
				t.Fatalf("codec rejection was not durably committed: found=%t error=%v", found, err)
			}
			if err := head.Validate(); err != nil {
				t.Fatalf("codec rejection corrupted the retained tree: %v", err)
			}
		})
	}
}

func codecProgram[I, O any](t *testing.T) run.Program[run.Payload, run.Payload] {
	t.Helper()
	input, err := jsonschema.For[I]()
	if err != nil {
		t.Fatal(err)
	}
	output, err := jsonschema.For[O]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{
		Name: "codec_child", Description: "Return the admitted wire value without the consuming stage's Go codec.",
		InputSchema: input, OutputSchema: output,
	})
	if err != nil {
		t.Fatal(err)
	}
	return mustProgram(t, codecDefinition{descriptor: descriptor}, "codec-child")
}
func codecBranches[I, O any](t *testing.T) []ForkBranch {
	t.Helper()
	return []ForkBranch{{ID: "child", Child: run.ChildBinding{Program: codecProgram[I, O](t), Budget: mustBudget(t)}}}
}

type codecDefinition struct{ descriptor run.Descriptor }

func (c codecDefinition) Descriptor() run.Descriptor                           { return c.descriptor }
func (codecDefinition) ChildPrograms() []run.Program[run.Payload, run.Payload] { return nil }
func (codecDefinition) Start(input run.Payload) (run.Execution, error) {
	return codecExecution{input: input}, nil
}
func (codecDefinition) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	input, err := run.ParsePayload(state.Payload())
	if err != nil {
		return nil, err
	}
	return codecExecution{input: input}, nil
}

type codecExecution struct{ input run.Payload }

func (c codecExecution) Snapshot() (run.ExecutionState, error) {
	return run.ParseExecutionState("codec_child", c.input.JSON())
}
func (c codecExecution) Step(ctx context.Context, _ run.Turn) (run.Transition, error) {
	if err := ctx.Err(); err != nil {
		return run.Transition{}, err
	}
	return run.Complete(0, c.input)
}
