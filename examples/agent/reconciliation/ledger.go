package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

type note struct {
	Text string `json:"text"`
}

type receipt struct {
	IdempotencyKey string
	Note           note
	Accepted       bool
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
	receipts     map[string]receipt
	attempts     int
	queries      int
}

func (r *remoteLedger) submit(ctx context.Context, key string, input note) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts++
	if _, exists := r.receipts[key]; exists {
		return errors.New("remote: unexpected repeated write")
	}
	if r.receipts == nil {
		r.receipts = make(map[string]receipt)
	}
	r.receipts[key] = receipt{IdempotencyKey: key, Note: input, Accepted: !r.reject}
	return nil
}

func (r *remoteLedger) lookup(ctx context.Context, key string) (receipt, bool, error) {
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
		result, found := r.receipts[key]
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
