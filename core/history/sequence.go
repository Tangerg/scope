package history

import (
	"fmt"
	"sync"
	"time"
)

// Sequence assigns the positions a store writes alongside its messages.
//
// [Writer] promises that one Write reads back in argument order, and a store
// that derives positions from the wall clock cannot keep that promise by
// itself: a clock that steps backward — an NTP correction, a suspended host —
// would place a later batch before an earlier one, and messages inside a single
// batch can land on the same instant however fine the clock's resolution. A
// Sequence hands out a contiguous, strictly increasing run per call and never
// reissues a position it has already given out, which is what makes the
// ordering the contract advertises hold.
//
// A Sequence orders the writes made through one value. Ordering across separate
// Store instances stays implementation-defined, as [Writer] says.
type Sequence struct {
	mu     sync.Mutex
	last   int64
	stride int64
}

// NewSequence spaces consecutive positions stride apart.
//
// Pass one nanosecond unless the store's position type is coarser than the
// clock: a Cassandra TIMEUUID advances in 100-nanosecond ticks, so reserving at
// nanosecond spacing there would let distinct positions collapse onto one
// identifier.
func NewSequence(stride time.Duration) (*Sequence, error) {
	if stride <= 0 {
		return nil, fmt.Errorf("history: sequence stride must be positive, got %s", stride)
	}
	return &Sequence{stride: int64(stride)}, nil
}

// Reserve returns the first position of a contiguous run of count positions,
// each one stride after the last. Successive calls strictly increase even when
// the wall clock does not.
//
// A count below one is a caller bug rather than a runtime condition — every
// store reserves the length of a batch it has already refused to build empty —
// so it panics instead of widening every call site with an unreachable branch.
func (s *Sequence) Reserve(count int) int64 {
	return s.reserveAt(time.Now().UnixNano(), count)
}

// reserveAt is Reserve with the clock supplied, so the regression guard can be
// exercised without waiting for a real clock to move backward.
func (s *Sequence) reserveAt(candidate int64, count int) int64 {
	if count <= 0 {
		panic("history: sequence count must be positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	first := candidate
	if first <= s.last {
		first = s.last + 1
	}
	s.last = first + int64(count)*s.stride - 1
	return first
}
