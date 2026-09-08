package history

import (
	"sync"
	"testing"
	"time"
)

// The regression guard is the whole reason a Sequence exists: a clock that
// steps backward must not place a later batch before an earlier one.
func TestSequenceIgnoresAClockThatMovesBackward(t *testing.T) {
	t.Parallel()

	sequence, err := NewSequence(time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	first := sequence.reserveAt(1_000, 3)
	if first != 1_000 {
		t.Fatalf("first reservation = %d, want 1000", first)
	}
	// The clock has gone back behind the whole first run.
	second := sequence.reserveAt(900, 2)
	if second != 1_003 {
		t.Fatalf("second reservation = %d, want 1003, the position after the first run", second)
	}
	third := sequence.reserveAt(1_003, 1)
	if third != 1_005 {
		t.Fatalf("third reservation = %d, want 1005; a repeated instant must not reissue a position", third)
	}
}

// A batch owns a contiguous run so its messages read back in argument order.
func TestSequenceReservesContiguousRuns(t *testing.T) {
	t.Parallel()

	for _, sample := range []struct {
		name   string
		stride time.Duration
		count  int
		want   int64
	}{
		{name: "nanosecond spacing", stride: time.Nanosecond, count: 4, want: 1_004},
		{name: "TIMEUUID spacing", stride: 100 * time.Nanosecond, count: 4, want: 1_400},
	} {
		t.Run(sample.name, func(t *testing.T) {
			sequence, err := NewSequence(sample.stride)
			if err != nil {
				t.Fatal(err)
			}
			if first := sequence.reserveAt(1_000, sample.count); first != 1_000 {
				t.Fatalf("first reservation = %d, want 1000", first)
			}
			// The next run starts past every position the first run covers.
			if next := sequence.reserveAt(1_000, 1); next != sample.want {
				t.Fatalf("next reservation = %d, want %d", next, sample.want)
			}
		})
	}
}

// Concurrent writers must never share a position.
func TestSequenceHandsOutDisjointRunsConcurrently(t *testing.T) {
	t.Parallel()

	sequence, err := NewSequence(time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	const writers, perWriter = 16, 8

	positions := make(chan int64, writers*perWriter)
	var group sync.WaitGroup
	group.Add(writers)
	for range writers {
		go func() {
			defer group.Done()
			first := sequence.Reserve(perWriter)
			for offset := range int64(perWriter) {
				positions <- first + offset
			}
		}()
	}
	group.Wait()
	close(positions)

	seen := make(map[int64]struct{}, writers*perWriter)
	for position := range positions {
		if _, duplicate := seen[position]; duplicate {
			t.Fatalf("position %d was handed out twice", position)
		}
		seen[position] = struct{}{}
	}
	if len(seen) != writers*perWriter {
		t.Fatalf("distinct positions = %d, want %d", len(seen), writers*perWriter)
	}
}

// A stride that cannot separate two positions is a configuration error.
func TestNewSequenceRejectsNonPositiveStride(t *testing.T) {
	t.Parallel()

	for _, stride := range []time.Duration{0, -time.Nanosecond} {
		if _, err := NewSequence(stride); err == nil {
			t.Fatalf("NewSequence(%s) = nil error, want a stride error", stride)
		}
	}
}
