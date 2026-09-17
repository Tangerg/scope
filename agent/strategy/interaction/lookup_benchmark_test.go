package interaction

import (
	"fmt"
	"testing"

	"github.com/Tangerg/scope/agent"
)

func BenchmarkDelegateLookup(b *testing.B) {
	for _, count := range []int{16, 64, 256, 1024} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			prototype := fuzzInteractionDefinition(b).delegates[0]
			delegates := make([]Delegate, count)
			names := make([]string, count)
			for index := range count {
				name := fmt.Sprintf("delegate_%04d", index)
				delegates[index] = prototype
				delegates[index].definition.Name = name
				names[index] = name
			}
			definition, err := NewDefinition(DefinitionConfig{Name: "benchmark.delegates", Description: "Measure bound Delegate lookup.", MaxModelCalls: agent.NewQuota(1), Delegates: delegates})
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				for _, name := range names {
					if _, found := definition.delegate(name); !found {
						b.Fatal(name)
					}
				}
			}
		})
	}
}
