package history

import "fmt"

// CommitError retains an already completed generation's immutable append, so
// callers can retry persistence without invoking the model again. Receipt keeps
// acknowledged commit evidence even if an ancillary operation also failed.
type CommitError struct {
	batch   Batch
	receipt Receipt
	cause   error
}

func (c *CommitError) Error() string {
	return fmt.Sprintf("history: append %q to conversation %q after model completion: %v", c.batch.ID(), c.batch.ConversationID(), c.cause)
}

func (c *CommitError) Unwrap() error    { return c.cause }
func (c *CommitError) Batch() Batch     { return c.batch }
func (c *CommitError) Receipt() Receipt { return c.receipt }
