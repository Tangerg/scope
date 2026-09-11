package collaboration

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func BenchmarkCollaborationReplayBoundary(b *testing.B) {
	definition, _ := fixture(func(_ context.Context, turn Turn) (Decision, error) {
		return finish(turn, "done"), nil
	}, echo())
	for _, size := range []int{1 << 10, 64 << 10} {
		b.Run(fmt.Sprintf("input_bytes_%d", size), func(b *testing.B) {
			initial := require(require(definition.Start(input(strings.Repeat("x", size)))).Snapshot())
			b.ReportAllocs()
			for b.Loop() {
				execution, err := definition.Restore(initial)
				if err != nil {
					b.Fatal(err)
				}
				transition, err := execution.Step(b.Context(), nil)
				if err != nil || !transition.Valid() {
					b.Fatalf("Step = %v, %v", transition, err)
				}
				state, err := execution.Snapshot()
				if err != nil {
					b.Fatal(err)
				}
				if _, err := definition.Restore(state); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
