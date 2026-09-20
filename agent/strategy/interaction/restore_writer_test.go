package interaction_test

import (
	"context"
	"errors"
	"testing"

	agent "github.com/Tangerg/scope/agent"
)

func retireTestWriter(t *testing.T, engine *agent.Engine, process *agent.Process) {
	t.Helper()
	_ = process.Kill(context.Background(), "release retired test writer")
	if err := process.Join(context.Background()); err != nil && !errors.Is(err, agent.ErrTreeIncarnationConflict) {
		t.Error(err)
	}
	if err := engine.Close(context.Background()); err != nil {
		t.Error(err)
	}
}
