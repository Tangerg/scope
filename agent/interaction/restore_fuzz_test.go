package interaction

import (
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/tool"
)

func FuzzInteractionRestoreRoundTrip(f *testing.F) {
	tools := toolSetTest(f, scriptTool{name: "echo", classify: func(tool.Invocation) (string, bool) { return "", true }})
	definition := mustValue(newDefinition(Config{Name: "test.restore", Description: "Restore recorded interaction progress.", Tools: tools, MaxModelCalls: run.NewQuota(4), MaxConcurrentToolCalls: 2}))
	reference := testTurn(3).EffectID(0)
	seeds := []executionState{
		{WorkingContext: testInput()},
		{WorkingContext: testInput(), ModelCallCount: 1, PendingEffect: testTurn(1).EffectID(0)},
		{WorkingContext: testInput(), ModelCallCount: 1, ToolRound: &toolCallRound{
			Response: callResponse(requested("echo", "call")),
			Batch:    &callBatch{Kind: callTool, Invocations: []*callInvocation{{Reference: reference, Pending: &reference}}},
		}},
	}
	states := make([]run.ExecutionState, len(seeds))
	for index, seed := range seeds {
		states[index] = mustValue(seed.snapshot())
		if _, err := definition.Restore(f.Context(), states[index]); err != nil {
			f.Fatalf("seed %d is not a restorable state: %v", index, err)
		}
	}
	conformancetest.FuzzRestoreRoundTrip(f, definition, executionStateKind, states...)
}
