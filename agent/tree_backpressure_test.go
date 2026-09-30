package agent

import "testing"

func TestTreeCommandCapacityAppliesDuringCommitAndFreeze(t *testing.T) {
	for _, barrier := range []string{"commit", "freeze"} {
		t.Run(barrier, func(t *testing.T) {
			runtime := newTreeRuntime(&Engine{}, ProcessID{}, DefaultTreeLimits(), t.Context())
			if barrier == "commit" {
				runtime.writer.inFlight = &treeCommit{}
			} else {
				// A delivered freeze retains no acquisition: its acquirer already
				// holds the barrier and is owed no further answer.
				runtime.freeze.active = &activeTreeFreeze{freeze: &treeFreeze{runtime: runtime}}
			}
			for range treeCommandBufferCapacity {
				runtime.processCommands <- processTreeCommand{}
				if runtime.tryProcessCommand() {
					t.Fatal("barrier drained a Process command into unbounded storage")
				}
			}
			select {
			case runtime.processCommands <- processTreeCommand{}:
				t.Fatal("command beyond the configured capacity was accepted")
			default:
			}
			if barrier == "freeze" {
				response := make(chan error, 1)
				runtime.freezeCommands <- releaseFreezeCommand{freeze: runtime.freeze.active.freeze, response: response}
				runtime.waitForWork()
				if err := <-response; err != nil || runtime.freeze.engaged() {
					t.Fatalf("full Process queue prevented freeze release: %v", err)
				}
			}
		})
	}
}
