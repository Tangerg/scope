package interaction

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/tool"
)

// ParseToolProgress decodes one Delta a tool call Effect published. Like a
// model response Delta it is observational and never restores a call.
func ParseToolProgress(payload json.RawMessage) (tool.Progress, error) {
	wire, err := jsonwire.Decode[toolProgressWire](payload)
	if err != nil {
		return tool.Progress{}, fmt.Errorf("interaction: decode tool progress: %w", err)
	}
	if err := wire.Progress.Validate(); err != nil {
		return tool.Progress{}, fmt.Errorf("interaction: tool progress: %w", err)
	}
	return wire.Progress, nil
}

type toolProgressWire struct {
	Progress tool.Progress `json:"progress"`
}

// deltaProgress publishes a tool's progress as Deltas of its Effect.
type deltaProgress struct{ emit run.DeltaEmitter }

func (d deltaProgress) ReportProgress(_ context.Context, progress tool.Progress) error {
	payload, err := run.EncodePayload(toolProgressWire{Progress: progress})
	if err != nil {
		return fmt.Errorf("interaction: encode tool progress: %w", err)
	}
	d.emit(payload.JSON())
	return nil
}
