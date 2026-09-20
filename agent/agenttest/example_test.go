package agenttest_test

import (
	"fmt"

	"github.com/Tangerg/scope/agent"
)

func Example_memoryCommitter() {
	committer := agent.NewMemoryTreeCommitter()
	var contract agent.TreeCommitter = committer

	fmt.Printf("%T\n", contract)
	// Output:
	// *agent.MemoryTreeCommitter
}
