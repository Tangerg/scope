package run

import (
	"errors"
	"sync"
)

var ErrObservationGap = errors.New("run: observation gap; resubscribe from an acknowledged snapshot")
var ErrObservationCapacity = errors.New("run: observation capacity must be positive")

// Cursor identifies a position in one live tree activation. Positions are not
// durable event IDs: recovery of the same root creates a different stream.
type Cursor struct {
	RootID        ProcessID         `json:"root_id"`
	IncarnationID TreeIncarnationID `json:"incarnation_id"`
	Position      uint64            `json:"position"`
}

// Observation carries either an acknowledged Snapshot or one observed
// Event. Persist Cursor only after consuming its value.
type Observation struct {
	Cursor   Cursor
	Snapshot TreeSnapshot
	Event    Event
}

type treeStream struct {
	mu     sync.Mutex
	latest Observation
	// Framework events only; snapshot publications also advance cursor.Position.
	published uint64
	readers   map[*eventReader]struct{}
	stopped   bool
}

type eventReader struct {
	queue chan Observation
	// gap is written before queue is closed; read it only after observing closure.
	gap bool
}

func (t *treeStream) acknowledged(head TreeSnapshot) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.latest.Snapshot = head
	t.latest.Cursor.RootID = head.RootID()
	t.latest.Cursor.IncarnationID = head.IncarnationID()
	t.offerLocked(Observation{Snapshot: head})
}

func (t *treeStream) publish(e Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.published = saturatingCountAdd(t.published, 1)
	t.offerLocked(Observation{Event: e})
}

func (t *treeStream) offerLocked(value Observation) {
	t.latest.Cursor.Position++
	value.Cursor = t.latest.Cursor
	for r := range t.readers {
		select {
		case r.queue <- value:
		default:
			r.gap = true
			close(r.queue)
			delete(t.readers, r)
		}
	}
}

func (t *treeStream) finish() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopped = true
	for r := range t.readers {
		close(r.queue)
		delete(t.readers, r)
	}
}

func (t *treeStream) attach(after Cursor, capacity int) (Observation, *eventReader, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if after != (Cursor{}) && after != t.latest.Cursor {
		return Observation{}, nil, ErrObservationGap
	}
	r := &eventReader{queue: make(chan Observation, capacity)}
	if t.stopped {
		close(r.queue)
	} else {
		if t.readers == nil {
			t.readers = make(map[*eventReader]struct{})
		}
		t.readers[r] = struct{}{}
	}
	return t.latest, r, nil
}

func (t *treeStream) detach(r *eventReader) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.readers, r)
}

func (t *treeStream) publishedEvents() uint64 { t.mu.Lock(); defer t.mu.Unlock(); return t.published }
