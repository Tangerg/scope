package history

import (
	"errors"
	"fmt"
)

var ErrInvalidWriteOutcome = errors.New("history: invalid write outcome")

// WriteOutcome describes the acknowledged facts of one Write attempt. It never
// implies idempotency or rollback. The zero value confirms that nothing was
// written. Accepted counts the confirmed prefix in argument order. Uncertain
// means additional messages after that prefix may have been stored; the caller
// must reconcile them before retrying. Otherwise all remaining messages are
// confirmed unwritten. All messages may be accepted even if ancillary work fails.
//
// Reconciling an uncertain suffix is not something Reader can complete on its
// own: a stored message with identical content from a concurrent append is
// indistinguishable, and a change in message count does not prove whether this
// attempt committed. These interfaces intentionally carry no stable append
// identity, so exact automatic reconciliation requires backend-specific
// evidence, such as a store that assigns durable append identities; this
// contract does not provide it.
type WriteOutcome struct {
	Accepted  int
	Uncertain bool
}

// ValidateFor checks the outcome against the attempted batch size and error.
// Successful calls must acknowledge the full batch without uncertainty.
func (w WriteOutcome) ValidateFor(count int, err error) error {
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
