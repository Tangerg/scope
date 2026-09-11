package agent

import (
	"fmt"
	"testing"
)

// Each parent waits for two children and only the first child has completed.
// Notifications cannot satisfy the wait, so measurements isolate owner work
// without mailbox growth, execution, or storage acknowledgment.
func BenchmarkChildWaitNotification(b *testing.B) {
	for _, parents := range []int{1, 32, 128} {
		b.Run(fmt.Sprintf("parents_%d", parents), func(b *testing.B) {
			runtime, child := waitingOwnerFixture(b, parents)
			b.ReportAllocs()
			for b.Loop() {
				runtime.notifyChildWaits(child.handle.processID, ChildWaitBoundaryResult)
			}
		})
	}
}

func BenchmarkIdleTreeJoin(b *testing.B) {
	for _, parents := range []int{1, 32, 128} {
		b.Run(fmt.Sprintf("parents_%d", parents), func(b *testing.B) {
			runtime, _ := waitingOwnerFixture(b, parents)
			runtime.publishJoins()
			b.ReportAllocs()
			for b.Loop() {
				if runtime.publishJoins() {
					b.Fatal("unchanged tree published another join")
				}
			}
		})
	}
}
