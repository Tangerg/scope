package agent

import (
	"fmt"
	"testing"
)

func TestTreeCommandCapacityAppliesDuringCommitAndFreeze(t *testing.T) {
	for _, barrier := range []string{"commit", "freeze"} {
		for _, signal := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/signal=%t", barrier, signal), func(t *testing.T) {
				runtime := newTreeRuntime(&Engine{}, ProcessID{}, DefaultTreeLimits(), t.Context())
				if barrier == "commit" {
					runtime.writer.inFlight.Store(&treeCommit{})
				} else {
					// A delivered freeze retains no acquisition: its acquirer already
					// holds the barrier and is owed no further answer.
					runtime.freeze.active.Store(&activeTreeFreeze{freeze: &treeFreeze{runtime: runtime}})
				}
				commands, drain := runtime.controlCommands, runtime.tryControlCommand
				if signal {
					commands, drain = runtime.signalCommands, runtime.trySignalCommand
				}
				for range treeCommandBufferCapacity {
					commands <- processTreeCommand{}
					if drain() {
						t.Fatal("barrier drained a Process command into unbounded storage")
					}
				}
				select {
				case commands <- processTreeCommand{}:
					t.Fatal("command beyond the configured capacity was accepted")
				default:
				}
				if barrier == "freeze" {
					response := make(chan error, 1)
					runtime.freezeCommands <- releaseFreezeCommand{freeze: runtime.freeze.active.Load().freeze, response: response}
					runtime.waitForWork()
					if err := <-response; err != nil || runtime.freeze.engaged() {
						t.Fatalf("full Process queue prevented freeze release: %v", err)
					}
				}
			})
		}
	}
}
