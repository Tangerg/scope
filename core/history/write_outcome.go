package history

import (
	"errors"
	"fmt"
)

// ErrInvalidWriteOutcome identifies a Writer that returned contradictory facts.
var ErrInvalidWriteOutcome = errors.New("history: invalid write outcome")

// WriteOutcome describes the acknowledged facts of one Write attempt. It never
// implies idempotency or rollback. The zero value confirms that nothing was
// written. Accepted counts the confirmed prefix in argument order. Uncertain
// means additional messages after that prefix may have been stored; the caller
// must reconcile them before retrying. Otherwise all remaining messages are
// confirmed unwritten. All messages may be accepted even if ancillary work fails.
type WriteOutcome struct {
	Accepted  int
	Uncertain bool
}

// Validate checks the outcome against the attempted batch size and error.
// Successful calls must acknowledge the full batch without uncertainty.
func (w WriteOutcome) Validate(count int, err error) error {
	if count < 0 || w.Accepted < 0 || w.Accepted > count {
		return fmt.Errorf("%w: accepted %d of %d messages", ErrInvalidWriteOutcome, w.Accepted, count)
	}
	if w.Uncertain && w.Accepted == count {
		return fmt.Errorf("%w: no remaining message can be uncertain", ErrInvalidWriteOutcome)
	}
	if err == nil && (w.Uncertain || w.Accepted != count) {
		return fmt.Errorf("%w: success must acknowledge all %d messages", ErrInvalidWriteOutcome, count)
	}
	return nil
}
