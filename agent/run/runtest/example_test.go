package runtest_test

import (
	"fmt"

	"github.com/Tangerg/scope/agent/run"
)

func Example_memoryCommitter() {
	committer := run.NewMemoryStore()
	var contract run.Store = committer

	fmt.Printf("%T\n", contract)
	// Output:
	// *run.MemoryStore
}
