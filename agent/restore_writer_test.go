package agent_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/run"
)

func retireTestWriter(t *testing.T, engine *run.Runtime, process *run.Handle[run.Payload]) {
	t.Helper()
	_ = process.Kill(context.Background(), "release retired test writer")
	if err := process.Join(context.Background()); err != nil && !errors.Is(err, run.ErrTreeIncarnationConflict) {
		t.Error(err)
	}
	if err := engine.Close(context.Background()); err != nil {
		t.Error(err)
	}
}
