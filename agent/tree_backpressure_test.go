package agent

import "testing"

func TestTreeCommandCapacityAppliesDuringCommitAndFreeze(t *testing.T) {
	for _, barrier := range []string{"commit", "freeze"} {
		t.Run(barrier, func(t *testing.T) {
			runtime := newTreeRuntime(&Engine{}, ProcessID{}, t.Context())
			if barrier == "commit" {
				runtime.commit = &treeCommit{}
			} else {
				runtime.freeze = &activeTreeFreeze{ready: true, freeze: &treeFreeze{runtime: runtime}, acquisition: &treeFreezeAcquisition{canceled: make(chan struct{})}}
			}
			for range treeCommandBufferCapacity {
				runtime.commands <- treeCommand{kind: treeCommandProcess}
				if runtime.tryCommand() {
					t.Fatal("barrier drained a Process command into unbounded storage")
				}
			}
			select {
			case runtime.commands <- treeCommand{kind: treeCommandProcess}:
				t.Fatal("command beyond the configured capacity was accepted")
			default:
			}
			if barrier == "freeze" {
				response := make(chan error, 1)
				runtime.controls <- treeCommand{kind: treeCommandReleaseFreeze, freeze: runtime.freeze.freeze, response: response}
				runtime.waitForWork()
				if err := <-response; err != nil || runtime.freeze != nil {
					t.Fatalf("full Process queue prevented freeze release: %v", err)
				}
			}
		})
	}
}
