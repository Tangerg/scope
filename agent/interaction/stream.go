package interaction

import (
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
)

// ModelResponseDelta is one validated provider-neutral streaming increment.
// It is observational and never a source for final Output or restoration.
type ModelResponseDelta struct {
	delta chat.ResponseDelta
}

func ParseModelResponseDelta(payload run.Payload) (ModelResponseDelta, error) {
	wire, err := payload.Decode[modelResponseDeltaWire]()
	if err != nil {
		return ModelResponseDelta{}, fmt.Errorf("interaction: decode model response Delta: %w", err)
	}
	if err := wire.ResponseDelta.Validate(); err != nil {
		return ModelResponseDelta{}, fmt.Errorf("interaction: model response Delta: %w", err)
	}
	return ModelResponseDelta{delta: wire.ResponseDelta}, nil
}

// ResponseDelta returns an independently owned transport increment.
func (m ModelResponseDelta) ResponseDelta() *chat.ResponseDelta {
	return m.delta.Clone()
}

type modelResponseDeltaWire struct {
	ResponseDelta chat.ResponseDelta `json:"response_delta"`
}

func encodeModelResponseDelta(delta *chat.ResponseDelta) (run.Payload, error) {
	if delta == nil {
		return run.Payload{}, errors.New("interaction: cannot encode a nil model response Delta")
	}
	payload, err := run.EncodePayload(modelResponseDeltaWire{ResponseDelta: *delta})
	if err != nil {
		return run.Payload{}, fmt.Errorf("interaction: encode model response Delta: %w", err)
	}
	return payload, nil
}
