package agent

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// deltaStream sequences the Deltas of one dispatch attempt. A Dispatcher may
// emit from any goroutine until its call returns; close then stops the
// stream, so no Delta follows the attempt it belongs to.
type deltaStream struct {
	observation   *observationBus
	context       context.Context
	processID     ProcessID
	effectID      EffectID
	incarnationID TreeIncarnationID
	attemptID     EffectAttemptID

	mu       sync.Mutex
	sequence uint64
	dropped  uint64
	closed   bool
}

func (d *deltaStream) emit(payload json.RawMessage) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	d.sequence++
	delta, err := newDelta(d.processID, d.effectID, d.incarnationID, d.attemptID, d.sequence, time.Now(), payload)
	if err != nil || !d.observation.offerDelta(d.context, delta) {
		d.dropped++
	}
}

// emitter is nil for a nil stream, so a Dispatcher sees no emitter when no
// DeltaListener is configured.
func (d *deltaStream) emitter() DeltaEmitter {
	if d == nil {
		return nil
	}
	return d.emit
}

// close returns how many increments validation or the bounded queue rejected.
func (d *deltaStream) close() uint64 {
	if d == nil {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	return d.dropped
}
