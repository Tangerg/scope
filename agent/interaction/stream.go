package interaction

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

// ModelResponseDelta is one validated provider-neutral streaming increment.
// It is observational and never a source for final Output or restoration.
type ModelResponseDelta struct {
	delta chat.ResponseDelta
}

// ParseModelResponseDelta strictly decodes an Interaction model Delta payload.
func ParseModelResponseDelta(payload json.RawMessage) (ModelResponseDelta, error) {
	var wire modelResponseDeltaWire
	if err := jsonv2.Unmarshal(payload, &wire, jsonv2.RejectUnknownMembers(true)); err != nil {
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
	payload, err := json.Marshal(modelResponseDeltaWire{
		ResponseDelta: *delta,
	})
	if err != nil {
		return nil, fmt.Errorf("interaction: encode model response Delta: %w", err)
	}
	return payload, nil
}

func (d *Dispatcher) callModel(
	ctx context.Context,
	request *chat.Request,
	emit agent.DeltaEmitter,
) (*chat.Response, error) {
	if d.streamer == nil {
		return d.model.Call(ctx, request)
	}
	var accumulator chat.ResponseAccumulator
	seen := false
	sequence := d.streamer.Stream(ctx, request)
	if sequence == nil {
		return nil, errors.New("model streamer returned a nil sequence")
	}
	for delta, err := range sequence {
		if err != nil {
			return nil, err
		}
		if delta == nil {
			return nil, errors.New("model stream yielded a nil response Delta")
		}
		if err := accumulator.Add(delta); err != nil {
			return nil, fmt.Errorf("accumulate model stream: %w", err)
		}
		payload, err := encodeModelResponseDelta(delta)
		if err != nil {
			return nil, err
		}
		seen = true
		if emit != nil {
			emit(payload)
		}
	}
	if !seen {
		return nil, errors.New("model stream ended without a response Delta")
	}
	response, err := accumulator.Response()
	if err != nil {
		return nil, fmt.Errorf("complete model stream: %w", err)
	}
	return response, nil
}
