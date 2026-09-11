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
