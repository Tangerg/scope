package history

import (
	"fmt"

	"github.com/Tangerg/scope/core/chat"
)

// CommitError means model generation completed but history persistence failed.
// It owns the exact batch submitted to Write, so recovery need not call the
// model again. Outcome governs whether a suffix is safe to submit: uncertain
// writes require reconciliation, and Accepted messages must not be appended again.
// During streaming this error can follow the terminal model delta.
type CommitError struct {
	conversationID ConversationID
	messages       []chat.Message
	outcome        WriteOutcome
	cause          error
}

func (c *CommitError) Error() string {
	return fmt.Sprintf("history: commit conversation %q after model completion: %v", c.conversationID, c.cause)
}

func (c *CommitError) Unwrap() error { return c.cause }

func (c *CommitError) ConversationID() ConversationID { return c.conversationID }

func (c *CommitError) Outcome() WriteOutcome { return c.outcome }

// Messages returns an independently owned copy of the complete attempted batch.
func (c *CommitError) Messages() []chat.Message {
	messages := make([]chat.Message, len(c.messages))
	for index := range c.messages {
		messages[index] = c.messages[index].Clone()
	}
	return messages
}
