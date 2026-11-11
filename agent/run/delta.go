package run

import (
	"encoding/json"
	"math"
	"sync"
	"time"
)

const maxDeltaBytes = 1 << 20

type Delta struct {
	effect    EffectID
	epoch     Epoch
	sequence  uint64
	emittedAt time.Time
	payload   Payload
}

func (d Delta) ProcessID() ProcessID     { return d.effect.Process }
func (d Delta) EffectID() EffectID       { return d.effect }
func (d Delta) Epoch() Epoch             { return d.epoch }
func (d Delta) EffectSequence() uint64   { return d.sequence }
func (d Delta) EmittedAt() time.Time     { return d.emittedAt }
func (d Delta) Payload() json.RawMessage { return d.payload.JSON() }

func (d Delta) Valid() bool {
	return d.effect.Valid() && d.epoch > 0 && d.sequence > 0 && d.payload.Valid()
}

// One emitter serializes its producers. The channel is Host-owned and must stay
// open until Runtime.Close returns. Full channels drop increments, never facts.
type deltaEmitter struct {
	mu       sync.Mutex
	request  EffectRequest
	channel  chan<- Delta
	sequence uint64
}

func (d *deltaEmitter) emit(raw json.RawMessage) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sequence == math.MaxUint64 {
		return
	}
	d.sequence++
	normalized, err := normalizeJSON(raw, maxDeltaBytes)
	if err != nil {
		return
	}
	delta := Delta{effect: d.request.ID(), epoch: d.request.Epoch(), sequence: d.sequence, emittedAt: canonicalTime(time.Now()), payload: Payload{data: normalized}}
	select {
	case d.channel <- delta:
	default:
	}
}
