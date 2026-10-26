package workflow_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/workflow"
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
	child := mustDeployment(t, mustDefinition(t, "workflow.codec_child",
		mustTransform(t, "identity", func(_ context.Context, input int) (int, error) { return input, nil }),
	), "codec-child")
	branches := []workflow.ForkBranch{{ID: "child", Deployment: child, Budget: mustBudget(t)}}
	for _, test := range []struct {
		name       string
		input      any
		stage      func() (workflow.Stage, error)
		diagnostic string
	}{
		{"transform_input", 3, func() (workflow.Stage, error) {
			return workflow.Transform("work", func(context.Context, refusingDecode) (int, error) { return 0, nil })
		}, errDecodeValue.Error()},
		{"transform_output", 3, func() (workflow.Stage, error) {
			return workflow.Transform("work", func(context.Context, int) (refusingEncode, error) { return 0, nil })
		}, errEncodeValue.Error()},
		{"transform_output_schema", 3, func() (workflow.Stage, error) {
			return workflow.Transform("work", func(context.Context, int) (wrongShape, error) { return 0, nil })
		}, "output contract"},
		{"switch_input", 3, func() (workflow.Stage, error) {
			return workflow.Switch(workflow.SwitchConfig[refusingDecode]{ID: "work",
				Cases:  []workflow.SwitchCase{{ID: "child", Deployment: codecDeployment[refusingDecode, int](t), Budget: mustBudget(t)}},
				Select: func(context.Context, refusingDecode) (string, error) { return "child", nil },
			})
		}, errDecodeValue.Error()},
		{"fork_input", 3, func() (workflow.Stage, error) {
			return workflow.Fork(workflow.ForkConfig[refusingDecode, int, int]{ID: "work", Branches: codecBranches[refusingDecode, int](t), WindowSize: 1,
				Reduce: func(context.Context, refusingDecode, []int) (int, error) { return 0, nil },
			})
		}, errDecodeValue.Error()},
		{"fork_branch", 3, func() (workflow.Stage, error) {
			return workflow.Fork(workflow.ForkConfig[int, refusingDecode, int]{ID: "work", Branches: codecBranches[int, refusingDecode](t), WindowSize: 1,
				Reduce: func(context.Context, int, []refusingDecode) (int, error) { return 0, nil },
			})
		}, errDecodeValue.Error()},
		{"fork_output", 3, func() (workflow.Stage, error) {
			return workflow.Fork(workflow.ForkConfig[int, int, refusingEncode]{ID: "work", Branches: branches, WindowSize: 1,
				Reduce: func(context.Context, int, []int) (refusingEncode, error) { return 0, nil },
			})
		}, errEncodeValue.Error()},
		{"fork_output_schema", 3, func() (workflow.Stage, error) {
			return workflow.Fork(workflow.ForkConfig[int, int, wrongShape]{ID: "work", Branches: branches, WindowSize: 1,
				Reduce: func(context.Context, int, []int) (wrongShape, error) { return 0, nil },
			})
		}, "result contract"},
		{"map_input_decode", []int{3}, func() (workflow.Stage, error) {
			return workflow.Map(workflow.MapConfig[refusingDecode, int]{ID: "work", Deployment: codecDeployment[refusingDecode, int](t), Budget: mustBudget(t), WindowSize: 1, MaxItems: 1})
		}, errDecodeValue.Error()},
		{"map_input_encode", []int{3}, func() (workflow.Stage, error) {
			return workflow.Map(workflow.MapConfig[refusingEncode, int]{ID: "work", Deployment: codecDeployment[refusingEncode, int](t), Budget: mustBudget(t), WindowSize: 1, MaxItems: 1})
		}, errEncodeValue.Error()},
		{"map_input_schema", []int{3}, func() (workflow.Stage, error) {
			return workflow.Map(workflow.MapConfig[wrongShape, int]{ID: "work", Deployment: codecDeployment[wrongShape, int](t), Budget: mustBudget(t), WindowSize: 1, MaxItems: 1})
		}, "integer"},
		{"map_output_decode", []int{3}, func() (workflow.Stage, error) {
			return workflow.Map(workflow.MapConfig[int, refusingDecode]{ID: "work", Deployment: codecDeployment[int, refusingDecode](t), Budget: mustBudget(t), WindowSize: 1, MaxItems: 1})
		}, errDecodeValue.Error()},
		{"map_output_encode", []int{3}, func() (workflow.Stage, error) {
			return workflow.Map(workflow.MapConfig[int, refusingEncode]{ID: "work", Deployment: codecDeployment[int, refusingEncode](t), Budget: mustBudget(t), WindowSize: 1, MaxItems: 1})
		}, errEncodeValue.Error()},
		{"map_output_schema", []int{3}, func() (workflow.Stage, error) {
			return workflow.Map(workflow.MapConfig[int, wrongShape]{ID: "work", Deployment: codecDeployment[int, wrongShape](t), Budget: mustBudget(t), WindowSize: 1, MaxItems: 1})
		}, "result contract"},
		{"loop_decode", 3, func() (workflow.Stage, error) {
			return workflow.Loop(workflow.LoopConfig[refusingDecode]{ID: "work", Body: codecDeployment[refusingDecode, refusingDecode](t), Budget: mustBudget(t), MaxIterations: agent.NewQuota(1),
				Predicate: func(context.Context, refusingDecode) (bool, error) { return true, nil },
			})
		}, errDecodeValue.Error()},
		{"loop_encode", 3, func() (workflow.Stage, error) {
			return workflow.Loop(workflow.LoopConfig[refusingEncode]{ID: "work", Body: codecDeployment[refusingEncode, refusingEncode](t), Budget: mustBudget(t), MaxIterations: agent.NewQuota(1),
				Predicate: func(context.Context, refusingEncode) (bool, error) { return true, nil },
			})
		}, errEncodeValue.Error()},
		{"loop_output_schema", 3, func() (workflow.Stage, error) {
			return workflow.Loop(workflow.LoopConfig[wrongShape]{ID: "work", Body: codecDeployment[wrongShape, wrongShape](t), Budget: mustBudget(t), MaxIterations: agent.NewQuota(1),
				Predicate: func(context.Context, wrongShape) (bool, error) { return true, nil },
			})
		}, "integer"},
	} {
		t.Run(test.name, func(t *testing.T) {
			stage, err := test.stage()
			if err != nil {
				t.Fatal(err)
			}
			deployment := mustDeployment(t, mustDefinition(t, "workflow.codec_"+test.name, stage), test.name)
			store := agent.NewMemoryTreeCommitter()
			engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: store})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
					t.Error(closeErr)
				}
			})
			input, err := agent.EncodePayload(test.input)
			if err != nil {
				t.Fatal(err)
			}
			result, err := engine.Run(t.Context(), deployment, input)
			if err != nil {
				t.Fatal(err)
			}
			failure, failed := result.Termination().Failure()
			if !failed || result.Termination().Status() != agent.StatusFailed || !strings.Contains(failure.Message(), test.diagnostic) {
				t.Fatalf("codec failure was not preserved: %s, %v", result.Termination().Status(), failure)
			}
			if _, completed := result.Termination().Output(); completed {
				t.Fatal("invalid typed value escaped as a completed output")
			}
			head, found, err := store.LoadTree(t.Context(), result.ProcessID())
			if err != nil || !found {
				t.Fatalf("codec rejection was not durably committed: found=%t error=%v", found, err)
			}
			if _, err := agent.ParseTreeSnapshot(head.JSON()); err != nil {
				t.Fatalf("codec rejection corrupted the retained tree: %v", err)
			}
		})
	}
}

func codecDeployment[I, O any](t *testing.T) agent.Deployment {
	t.Helper()
	input, err := jsonschema.For[I]()
	if err != nil {
		t.Fatal(err)
	}
	output, err := jsonschema.For[O]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name: "workflow.codec_child", Description: "Return the admitted wire value without the consuming stage's Go codec.",
		InputSchema: input, OutputSchema: output,
	})
	if err != nil {
		t.Fatal(err)
	}
	return mustDeployment(t, codecDefinition{descriptor: descriptor}, "codec-child")
}
func codecBranches[I, O any](t *testing.T) []workflow.ForkBranch {
	t.Helper()
	return []workflow.ForkBranch{{ID: "child", Deployment: codecDeployment[I, O](t), Budget: mustBudget(t)}}
}

type codecDefinition struct{ descriptor agent.Descriptor }

func (c codecDefinition) Descriptor() agent.Descriptor       { return c.descriptor }
func (codecDefinition) ChildDeployments() []agent.Deployment { return nil }
func (codecDefinition) Start(input agent.Payload) (agent.Execution, error) {
	return codecExecution{input: input}, nil
}
func (codecDefinition) Restore(ctx context.Context, state agent.ExecutionState) (agent.Execution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	input, err := agent.ParsePayload(state.Payload())
	if err != nil {
		return nil, err
	}
	return codecExecution{input: input}, nil
}

type codecExecution struct{ input agent.Payload }

func (c codecExecution) Snapshot() (agent.ExecutionState, error) {
	return agent.ParseExecutionState("workflow.codec_child", c.input.JSON())
}
func (c codecExecution) Step(ctx context.Context, _ []agent.Signal) (agent.Transition, error) {
	if err := ctx.Err(); err != nil {
		return agent.Transition{}, err
	}
	return agent.Complete(0, c.input)
}
