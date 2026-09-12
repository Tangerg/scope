package history

import (
	"context"
	"errors"

	"github.com/Tangerg/scope/core/chat"
)

var ErrNilStore = errors.New("history: nil store")

// Reader returns the messages to replay for one conversation. Implementations
// return a non-nil empty slice for an unknown conversation and transfer
// ownership of returned protocol values to the caller.
type Reader interface {
	// Read returns a detached snapshot in stored order. Unknown conversations
	// yield a non-nil empty slice; implementations honor ctx and never expose
	// mutable backing storage.
	Read(ctx context.Context, conversationID ConversationID) ([]chat.Message, error)
}

// Writer appends messages to one conversation. Implementations preserve the
// order of messages within each call, validate and snapshot them before
// returning, and prevent later caller mutation from altering stored history.
// The relative order of concurrent calls and writes issued through distinct
// Store instances is implementation-defined.
type Writer interface {
	// Write validates and snapshots the full argument batch before appending it
	// in argument order. The outcome must account for every acknowledged or
	// uncertain effect even on error; validation failures return the zero outcome.
	// A nil error acknowledges the complete batch.
	Write(ctx context.Context, conversationID ConversationID, messages ...chat.Message) (WriteOutcome, error)
}

// ReadWriter combines the capabilities required by components that replay and
// append history without owning retention or deletion policy.
type ReadWriter interface {
	Reader
	Writer
}

// Clearer removes every message for one conversation.
type Clearer interface {
	// Clear removes the complete conversation and is idempotent when it is
	// already absent. Implementations must honor ctx.
	Clear(ctx context.Context, conversationID ConversationID) error
}

// Store is the ordinary per-conversation read/write/clear contract. Optional
// cross-conversation or retention capabilities remain separate interfaces.
type Store interface {
	ReadWriter
	Clearer
}

// Lister enumerates unique conversation IDs in lexical order. Implementations
// return a non-nil empty slice when no conversations exist. Concurrent
// mutations may affect the result.
type Lister interface {
	// Conversations returns detached, unique identifiers in lexical order. An
	// empty store yields a non-nil empty slice; concurrent writes may appear or
	// not according to the backend's snapshot boundary.
	Conversations(ctx context.Context) ([]ConversationID, error)
}
