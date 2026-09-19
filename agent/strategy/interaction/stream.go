package interaction

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/internal/jsonwire"
	"github.com/Tangerg/scope/core/chat"
)

// ModelResponseDelta is one validated provider-neutral streaming increment.
// It is observational and never a source for final Output or restoration.
type ModelResponseDelta struct {
	delta chat.ResponseDelta
}

// ParseModelResponseDelta strictly decodes an Interaction model Delta payload.
func ParseModelResponseDelta(payload json.RawMessage) (ModelResponseDelta, error) {
	wire, err := jsonwire.Decode[modelResponseDeltaWire](payload)
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

func encodeModelResponseDelta(delta *chat.ResponseDelta) (json.RawMessage, error) {
	if delta == nil {
		return nil, errors.New("interaction: cannot encode a nil model response Delta")
	}
	payload, err := agent.EncodePayload(modelResponseDeltaWire{
		ResponseDelta: *delta,
	})
	if err != nil {
		return nil, fmt.Errorf("interaction: encode model response Delta: %w", err)
	}
	return payload.JSON(), nil
}
