package main

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/Tangerg/scope/agent"
)

type note struct {
	Text string `json:"text"`
}

type receipt struct {
	EffectID agent.EffectID
	Note     note
	Accepted bool
}

type queryAvailability uint8

const (
	queryAvailable queryAvailability = iota
	queryMissing
	queryUnavailable
)

// remoteLedger is the isolated external authority. The query fault can hide a
// real receipt, as a lagging read endpoint or outage would; neither changes the
// write or establishes a definite outcome for the caller.
type remoteLedger struct {
	mu           sync.Mutex
	reject       bool
	availability queryAvailability
	receipts     map[agent.EffectID]receipt
	attempts     int
	queries      int
}

func (r *remoteLedger) submit(ctx context.Context, id agent.EffectID, input note) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts++
	if _, exists := r.receipts[id]; exists {
		return errors.New("remote: unexpected repeated write")
	}
	if r.receipts == nil {
		r.receipts = make(map[agent.EffectID]receipt)
	}
	r.receipts[id] = receipt{EffectID: id, Note: input, Accepted: !r.reject}
	return nil
}

func (r *remoteLedger) lookup(ctx context.Context, id agent.EffectID) (receipt, bool, error) {
	if err := ctx.Err(); err != nil {
		return receipt{}, false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queries++
	switch r.availability {
	case queryMissing:
		return receipt{}, false, nil
	case queryUnavailable:
		return receipt{}, false, errors.New("remote: receipt query unavailable")
	case queryAvailable:
		result, found := r.receipts[id]
		return result, found, nil
	default:
		return receipt{}, false, fmt.Errorf("remote: invalid query availability %d", r.availability)
	}
}

type ledgerReport struct {
	Attempts int
	Queries  int
	Writes   int
}

func (r *remoteLedger) report() ledgerReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := ledgerReport{Attempts: r.attempts, Queries: r.queries}
	for _, outcome := range r.receipts {
		if outcome.Accepted {
			result.Writes++
		}
	}
	return result
}
