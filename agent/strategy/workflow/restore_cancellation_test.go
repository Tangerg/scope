package workflow

import (
	"context"
	"errors"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/internal/conformancetest"
)

func TestRestoreStopsBetweenFanoutChildren(t *testing.T) {
	state := executionState{ActiveFanoutWindow: []fanoutChildState{{}, {ChildProcessID: new(agent.ProcessID)}}}
	ctx, cancel := conformancetest.CancelAfterCheck(t.Context(), 2)
	defer cancel()
	if _, _, err := state.validateFanoutChildren(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("fan-out validation = %v, want cancellation before malformed second child", err)
	}
}
