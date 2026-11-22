package interaction

import (
	"context"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/tool"
)

// deltaProgress publishes a tool's progress as Deltas of its Effect.
type deltaProgress struct{ emit run.DeltaEmitter }

func (d deltaProgress) ReportProgress(_ context.Context, progress tool.Progress) error {
	payload, err := encodeToolProgressDelta(progress)
	if err != nil {
		return err
	}
	d.emit(payload)
	return nil
}
