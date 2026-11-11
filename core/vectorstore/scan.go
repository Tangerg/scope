package vectorstore

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// ScanRequest grants document enumeration, without a ranking vector or TopK.
// It never authorizes an implicit fallback from a native query. Backends must
// return no partial matches when the source exceeds the budget.
type ScanRequest struct {
	Budget ScanBudget
	Filter filter.Predicate
}

func (s ScanRequest) Validate() error {
	if err := s.Budget.Validate(); err != nil {
		return err
	}
	if s.Filter != nil {
		return s.Filter.Validate()
	}
	return nil
}

// ScanBudget is an explicit Host grant for a complete source read. Native query
// must never infer this grant from a filter. Each adapter documents what it
// measures as source bytes and the consistency of its scan.
type ScanBudget struct {
	MaxRecords int
	MaxBytes   int64
	Timeout    time.Duration
}

var ErrScanBudget = errors.New("vectorstore: complete scan exceeds the authorized budget")

func (s ScanBudget) Validate() error {
	if s.MaxRecords <= 0 || s.MaxRecords == math.MaxInt || s.MaxBytes <= 0 || s.MaxBytes == math.MaxInt64 || s.Timeout <= 0 {
		return errors.New("vectorstore: scan requires positive finite record, byte, and time budgets")
	}
	return nil
}

// ScanMeter tracks the host's remaining grant for one scan, including across
// concurrent native readers. The owner must Close it to release its deadline.
// Charge records one observed source record; adapters must bound native pages
// separately when their protocol can fetch more than one record at a time.
type ScanMeter struct {
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	records int
	bytes   int64
}

// Begin starts a fresh meter and deadline; it does not modify the grant.
func (s ScanBudget) Begin(ctx context.Context) (*ScanMeter, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	return &ScanMeter{ctx: ctx, cancel: cancel, records: s.MaxRecords, bytes: s.MaxBytes}, nil
}

func (s *ScanMeter) Context() context.Context { return s.ctx }
func (s *ScanMeter) Close()                   { s.cancel() }

// Charge checks the deadline and atomically charges one record and its native
// bytes before decoding, filtering or retaining it. No partial debit occurs.
func (s *ScanMeter) Charge(n int64) error {
	return s.ChargePage(1, n)
}

// ChargePage debits native rows and bytes together. Streaming protocols can
// first debit bounded wire bytes, then the decoded page's record count.
func (s *ScanMeter) ChargePage(records int, n int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if n < 0 || records < 0 || s.records < records || s.bytes < n {
		return ErrScanBudget
	}
	s.records -= records
	s.bytes -= n
	return nil
}

// RemainingBytes is an observation, not a reservation. A sequential reader can
// cap its next response with it; ChargePage still performs the atomic debit.
func (s *ScanMeter) RemainingBytes() int64 { s.mu.Lock(); defer s.mu.Unlock(); return s.bytes }
