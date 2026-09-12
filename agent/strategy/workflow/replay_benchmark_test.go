package workflow_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/workflow"
)

type replayValue struct {
	Steps int    `json:"steps"`
	Text  string `json:"text"`
}

// Exercise the same Step, Snapshot, Restore boundary that admits Engine work.
// Every callback result is new input; the current private state is unchanged
// between restoration, Step entry, and Snapshot entry.
func BenchmarkWorkflowReplayBoundary(b *testing.B) {
	const count = 16
	stages := make([]workflow.Stage, count)
	for index := range stages {
		stage, err := workflow.Transform(fmt.Sprintf("step.%d", index), func(_ context.Context, value replayValue) (replayValue, error) {
			value.Steps++
			return value, nil
		})
		if err != nil {
			b.Fatal(err)
		}
		stages[index] = stage
	}
	definition, err := workflow.NewDefinition(workflow.DefinitionConfig{
		Name: "benchmark.replay", Description: "Measure Workflow recovery admission.", Stages: stages,
	})
	if err != nil {
		b.Fatal(err)
	}
	for _, size := range []int{1 << 10, 64 << 10} {
		b.Run(fmt.Sprintf("bytes_%d", size), func(b *testing.B) {
			input, err := agent.EncodeInput(replayValue{Text: strings.Repeat("x", size)})
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				execution, startErr := definition.Start(input)
				if startErr != nil {
					b.Fatal(startErr)
				}
				for index := range count {
					transition, stepErr := execution.Step(b.Context(), nil)
					if stepErr != nil {
						b.Fatal(stepErr)
					}
					snapshot, snapshotErr := execution.Snapshot()
					if snapshotErr != nil {
						b.Fatal(snapshotErr)
					}
					execution, err = definition.Restore(snapshot)
					if err != nil {
						b.Fatal(err)
					}
					if index == count-1 {
						output, completed := transition.Output()
						var value replayValue
						if !completed || json.Unmarshal(output.JSON(), &value) != nil || value.Steps != count || len(value.Text) != size {
							b.Fatal("replay lost the completed Workflow result")
						}
					}
				}
			}
		})
	}
}

func BenchmarkMapReplayBoundary(b *testing.B) {
	for _, count := range []uint32{256, 1024, 4096} {
		for _, window := range []uint32{1, 8, 64} {
			b.Run(fmt.Sprintf("items_%d/window_%d", count, window), func(b *testing.B) {
				definition := mapReplayDefinition(b, count, window)
				values := make([]int, count)
				for index := range values {
					values[index] = index
				}
				input, err := agent.EncodeInput(values)
				if err != nil {
					b.Fatal(err)
				}
				execution, err := definition.Start(input)
				if err != nil {
					b.Fatal(err)
				}
				ready, err := execution.Snapshot()
				if err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				for b.Loop() {
					execution, err = definition.Restore(ready)
					if err != nil {
						b.Fatal(err)
					}
					if _, err := execution.Step(b.Context(), nil); err != nil {
						b.Fatal(err)
					}
					snapshot, err := execution.Snapshot()
					if err != nil {
						b.Fatal(err)
					}
					if _, err := definition.Restore(snapshot); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func mapReplayDefinition(t testing.TB, count, window uint32) *workflow.Definition {
	t.Helper()
	identity, err := workflow.Transform("identity", func(_ context.Context, value int) (int, error) { return value, nil })
	if err != nil {
		t.Fatal(err)
	}
	child, err := workflow.NewDefinition(workflow.DefinitionConfig{
		Name: "benchmark.map_child", Description: "Preserve one item.", Stages: []workflow.Stage{identity},
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: child, ImplementationDigest: agent.ComputeDigest([]byte("map-child")),
		ConfigurationDigest: agent.ComputeDigest([]byte("identity")),
	})
	if err != nil {
		t.Fatal(err)
	}
	stage, err := workflow.Map(workflow.MapConfig[int, int]{
		ID: "items", Deployment: deployment, Budget: agent.Budget{Steps: 10, Effects: 10, Signals: 10},
		WindowSize: window, MaxItems: count,
	})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := workflow.NewDefinition(workflow.DefinitionConfig{
		Name: "benchmark.map_replay", Description: "Measure Map recovery admission.", Stages: []workflow.Stage{stage},
	})
	if err != nil {
		t.Fatal(err)
	}
	return definition
}
