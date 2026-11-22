package interaction

import (
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

// Delta is one observational increment an interaction Effect published: a
// ModelResponseDelta from a model call or a ToolProgressDelta from a tool call.
// The set is closed, so a host routes its whole Delta stream through ParseDelta
// and a type switch. No Delta restores a call or establishes its result.
type Delta interface{ interactionDelta() }

// ModelResponseDelta is one validated provider-neutral streaming increment.
type ModelResponseDelta struct {
	delta chat.ResponseDelta
}

func (ModelResponseDelta) interactionDelta() {}

// ResponseDelta returns an independently owned transport increment.
func (m ModelResponseDelta) ResponseDelta() *chat.ResponseDelta {
	return m.delta.Clone()
}

// ToolProgressDelta is one validated progress report of a tool call.
type ToolProgressDelta struct {
	progress tool.Progress
}

func (ToolProgressDelta) interactionDelta() {}

func (t ToolProgressDelta) Progress() tool.Progress { return t.progress }

// ParseDelta decodes a Delta an interaction Effect published. It rejects the
// payloads other dispatchers publish as well as invalid ones.
func ParseDelta(payload run.Payload) (Delta, error) {
	wire, err := payload.Decode[deltaWire]()
	if err != nil {
		return nil, fmt.Errorf("interaction: decode Delta: %w", err)
	}
	return wire.delta()
}

func encodeModelResponseDelta(delta *chat.ResponseDelta) (run.Payload, error) {
	if delta == nil {
		return run.Payload{}, errors.New("interaction: cannot encode a nil model response Delta")
	}
	payload, err := run.EncodePayload(deltaWire{ResponseDelta: delta})
	if err != nil {
		return run.Payload{}, fmt.Errorf("interaction: encode model response Delta: %w", err)
	}
	return payload, nil
}

func encodeToolProgressDelta(progress tool.Progress) (run.Payload, error) {
	payload, err := run.EncodePayload(deltaWire{Progress: &progress})
	if err != nil {
		return run.Payload{}, fmt.Errorf("interaction: encode tool progress Delta: %w", err)
	}
	return payload, nil
}

// deltaWire names its variant by its single member. Deltas are never
// persisted, so the shape carries no compatibility obligation.
type deltaWire struct {
	ResponseDelta *chat.ResponseDelta `json:"response_delta,omitzero"`
	Progress      *tool.Progress      `json:"progress,omitzero"`
}

func (d deltaWire) delta() (Delta, error) {
	switch {
	case d.ResponseDelta != nil && d.Progress == nil:
		if err := d.ResponseDelta.Validate(); err != nil {
			return nil, fmt.Errorf("interaction: model response Delta: %w", err)
		}
		return ModelResponseDelta{delta: *d.ResponseDelta}, nil
	case d.Progress != nil && d.ResponseDelta == nil:
		if err := d.Progress.Validate(); err != nil {
			return nil, fmt.Errorf("interaction: tool progress Delta: %w", err)
		}
		return ToolProgressDelta{progress: *d.Progress}, nil
	default:
		return nil, errors.New("interaction: a Delta carries exactly one model response or tool progress")
	}
}
