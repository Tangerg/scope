package collaboration

import (
	"context"
	"fmt"
	"testing"

	"github.com/Tangerg/scope/agent"
)

func BenchmarkDecisionValidation(b *testing.B) {
	for _, count := range []int{64, 256, 1024} {
		definition, _ := fixture(func(_ context.Context, turn Turn) (Decision, error) { return finish(turn, "done"), nil }, echo())
		definition.maxConcurrentTasks = uint32(count)
		state := executionState{}
		decision := Decision{Mode: Continue, State: input("state")}
		for index := range count {
			state.Tasks = append(state.Tasks, Task{Request: request(fmt.Sprintf("old_%d", index), "test.echo", "input")})
			decision.Tasks = append(decision.Tasks, TaskRequest{Key: require(agent.ParseChildKey(fmt.Sprintf("new_%d", index))), Worker: "test.echo", Input: input("input")})
		}
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			for b.Loop() {
				if err := state.validateDecision(b.Context(), definition, decision); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
