package vectorstore

import (
	"errors"
	"fmt"
	"slices"
)

var ErrInvalidWriteOutcome = errors.New("vectorstore: invalid write outcome")

// WriteOutcome partitions the IDs of one failed indexing operation by what the
// backend acknowledged. It is plain data an adapter assembles while writing; a
// WriteError validates disjoint, duplicate-free partitions and owns its copy.
type WriteOutcome struct {
	// Acknowledged contains IDs for which the backend acknowledged every item
	// in its write group.
	Acknowledged []string
	// Rejected contains IDs for which the backend explicitly rejected the write.
	Rejected []string
	// Unconfirmed is the attempted group whose individual outcomes are unknown.
	Unconfirmed []string
	// AcknowledgedCount is a usable aggregate acknowledgment for Unconfirmed,
	// when available. It does not identify particular records in that group.
	// It must be between zero and len(Unconfirmed), inclusive.
	AcknowledgedCount *int
	// Unattempted contains IDs in groups the adapter never submitted.
	Unattempted []string
}

// Clone separates each partition from the source and from the other
// partitions, so appending to one outcome cannot rewrite another outcome.
func (w WriteOutcome) Clone() WriteOutcome {
	w.Acknowledged = slices.Clone(w.Acknowledged)
	w.Rejected = slices.Clone(w.Rejected)
	w.Unconfirmed = slices.Clone(w.Unconfirmed)
	w.Unattempted = slices.Clone(w.Unattempted)
	if w.AcknowledgedCount != nil {
		w.AcknowledgedCount = new(*w.AcknowledgedCount)
	}
	return w
}

// WriteError reports native acknowledgments received before an indexing operation
// failed. An acknowledgment is not a guarantee of search visibility. Unconfirmed
// IDs must not be replayed merely because the caller received an error. The
// error owns its outcome: adapter slices and reader copies never alias it.
type WriteError struct {
	outcome WriteOutcome
	err     error
}

// NewWriteError freezes a valid partition. Invalid evidence returns
// ErrInvalidWriteOutcome joined with cause, preserving any earlier write error.
func NewWriteError(cause error, outcome WriteOutcome) error {
	failure := &WriteError{outcome: outcome.Clone(), err: cause}
	if err := failure.validate(); err != nil {
		return errors.Join(cause, err)
	}
	return failure
}

func (w *WriteError) validate() error {
	seen := make(map[string]bool)
	for _, partition := range [][]string{w.outcome.Acknowledged, w.outcome.Rejected, w.outcome.Unconfirmed, w.outcome.Unattempted} {
		for _, id := range partition {
			if seen[id] {
				return fmt.Errorf("%w: duplicate ID %q", ErrInvalidWriteOutcome, id)
			}
			seen[id] = true
		}
	}
	if count := w.outcome.AcknowledgedCount; count != nil && (*count < 0 || *count > len(w.outcome.Unconfirmed)) {
		return fmt.Errorf("%w: aggregate acknowledgment %d outside [0, %d]", ErrInvalidWriteOutcome, *count, len(w.outcome.Unconfirmed))
	}
	return nil
}

// Outcome returns a copy the caller owns.
func (w *WriteError) Outcome() WriteOutcome { return w.outcome.Clone() }

func (w *WriteError) Error() string {
	return fmt.Sprintf("vectorstore: write: %v (acknowledged=%d, rejected=%d, unconfirmed=%d, unattempted=%d)",
		w.err, len(w.outcome.Acknowledged), len(w.outcome.Rejected), len(w.outcome.Unconfirmed), len(w.outcome.Unattempted))
}

func (w *WriteError) Unwrap() error { return w.err }
