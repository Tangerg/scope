package history

import (
	"fmt"
	"math"
	"sync"
	"time"
)

// Sequence reserves contiguous, strictly increasing positions despite wall-clock
// regression or repeated instants. It never reissues a position through the same
// value; ordering across separate Store instances remains implementation-defined.
type Sequence struct {
	mu     sync.Mutex
	last   int64
	stride int64
}

// NewSequence spaces positions by stride. Use one nanosecond unless the store
// needs a coarser unit, such as TIMEUUID's 100-nanosecond ticks.
func NewSequence(stride time.Duration) (*Sequence, error) {
	if stride <= 0 {
		return nil, fmt.Errorf("history: sequence stride must be positive, got %s", stride)
	}
	return &Sequence{stride: int64(stride)}, nil
}

// Reserve returns the first position of count positions spaced by stride.
// Successive calls strictly increase even when the wall clock does not.
// Non-positive counts and reservations exceeding int64 panic without consuming positions.
func (s *Sequence) Reserve(count int) int64 {
	return s.reserveAt(time.Now().UnixNano(), count)
}

func (s *Sequence) reserveAt(candidate int64, count int) int64 {
	if count <= 0 {
		panic("history: sequence count must be positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == math.MaxInt64 {
		panic("history: sequence exhausted")
	}
	first := candidate
	if first <= s.last {
		first = s.last + 1
	}
	if int64(count) > (math.MaxInt64-first+1)/s.stride {
		panic("history: sequence reservation exceeds int64")
	}
	s.last = first + (int64(count)*s.stride - 1)
	return first
}
