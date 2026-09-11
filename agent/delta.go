package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const maxDeltaBytes = 1 << 20

var ErrInvalidDelta = errors.New("agent: invalid delta")

// Delta is a bounded, best-effort stream increment from one Effect attempt.
// EffectSequence preserves emitter admission order. Delta is never replayed from a
// snapshot and never contributes to the authoritative final Output.
type Delta struct {
	processID      ProcessID
	effectID       EffectID
	incarnationID  TreeIncarnationID
	effectSequence uint64
	emittedAt      time.Time
	payload        json.RawMessage
}

func newDelta(
	processID ProcessID,
	effectID EffectID,
	incarnationID TreeIncarnationID,
	effectSequence uint64,
	emittedAt time.Time,
	payload json.RawMessage,
) (Delta, error) {
	if !processID.Valid() || !effectID.Valid() {
		return Delta{}, fmt.Errorf("%w: process ID and effect ID are required", ErrInvalidDelta)
	}
	if incarnationID != (TreeIncarnationID{}) && !incarnationID.Valid() {
		return Delta{}, fmt.Errorf("%w: tree incarnation is invalid", ErrInvalidDelta)
	}
	if effectSequence == 0 {
		return Delta{}, fmt.Errorf("%w: Effect sequence must be greater than zero", ErrInvalidDelta)
	}
	if emittedAt.IsZero() {
		return Delta{}, fmt.Errorf("%w: emission time is required", ErrInvalidDelta)
	}
	normalized, err := wireJSON.normalize(payload, maxDeltaBytes)
	if err != nil {
		return Delta{}, fmt.Errorf("%w: payload: %w", ErrInvalidDelta, err)
	}
	return Delta{
		processID:      processID,
		effectID:       effectID,
		incarnationID:  incarnationID,
		effectSequence: effectSequence,
		emittedAt:      emittedAt.Round(0).UTC(),
		payload:        normalized,
	}, nil
}

// ProcessID returns the Process that owns the Effect attempt.
func (d Delta) ProcessID() ProcessID { return d.processID }

// EffectID identifies the logical Effect and remains stable across replay attempts.
func (d Delta) EffectID() EffectID { return d.effectID }

// TreeIncarnationID returns the active durable writer that emitted this delta.
// Deltas from ephemeral trees return false.
func (d Delta) TreeIncarnationID() (TreeIncarnationID, bool) {
	return d.incarnationID, d.incarnationID.Valid()
}

// EffectSequence returns the one-based emitter admission order within the Effect
// attempt. Delivered sequences increase; dropped payloads leave gaps.
func (d Delta) EffectSequence() uint64 { return d.effectSequence }

// EmittedAt returns when the producer emitted the increment.
func (d Delta) EmittedAt() time.Time { return d.emittedAt }

// Payload returns an independently owned Strategy-defined increment.
func (d Delta) Payload() json.RawMessage { return bytes.Clone(d.payload) }

func (d Delta) Valid() bool {
	return d.processID.Valid() && d.effectID.Valid() && d.effectSequence > 0 &&
		(d.incarnationID == (TreeIncarnationID{}) || d.incarnationID.Valid()) &&
		!d.emittedAt.IsZero() && len(d.payload) > 0
}

func (d Delta) MarshalJSON() ([]byte, error) {
	if !d.Valid() {
		return nil, ErrInvalidDelta
	}
	return json.Marshal(deltaWire{
		ProcessID:      d.processID,
		EffectID:       d.effectID,
		EffectSequence: d.effectSequence,
		EmittedAt:      d.emittedAt,
		Payload:        d.payload,
		IncarnationID:  optionalTreeIncarnationID(d.incarnationID),
	})
}

func (d *Delta) UnmarshalJSON(data []byte) error {
	if d == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidDelta)
	}
	wire, err := wireJSON.decode[deltaWire](data)
	if err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidDelta, err)
	}
	value, err := newDelta(
		wire.ProcessID, wire.EffectID, treeIncarnationOrZero(wire.IncarnationID),
		wire.EffectSequence, wire.EmittedAt, wire.Payload,
	)
	if err != nil {
		return err
	}
	*d = value
	return nil
}

type deltaWire struct {
	ProcessID      ProcessID          `json:"process_id"`
	EffectID       EffectID           `json:"effect_id"`
	IncarnationID  *TreeIncarnationID `json:"tree_incarnation_id,omitempty"`
	EffectSequence uint64             `json:"effect_sequence"`
	EmittedAt      time.Time          `json:"emitted_at"`
	Payload        json.RawMessage    `json:"payload"`
}

func optionalTreeIncarnationID(value TreeIncarnationID) *TreeIncarnationID {
	if !value.Valid() {
		return nil
	}
	return &value
}
