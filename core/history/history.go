package history

import (
	"context"
	"errors"

	"github.com/Tangerg/scope/core/chat"
)

var ErrNilStore = errors.New("history: nil store")

type Reader interface {
	// Read returns a detached snapshot in stored order. Unknown conversations
	// yield a non-nil empty slice; implementations honor ctx and never expose
	// mutable backing storage.
	Read(ctx context.Context, conversationID ConversationID) ([]chat.Message, error)
}

// Writer leaves the relative order of concurrent calls, or of writes through
// separate Store instances, implementation-defined.
type Writer interface {
	// Write validates and snapshots the full argument batch before appending it
	// in argument order. The outcome must account for every acknowledged or
	// uncertain effect even on error; validation failures return the zero outcome.
	// A nil error acknowledges the complete batch.
	Write(ctx context.Context, conversationID ConversationID, messages ...chat.Message) (WriteOutcome, error)
}

type ReadWriter interface {
	Reader
	Writer
}

type Clearer interface {
	// Clear removes the complete conversation and is idempotent when it is
	// already absent. Implementations must honor ctx.
	Clear(ctx context.Context, conversationID ConversationID) error
}

type Store interface {
	ReadWriter
	Clearer
}

type Lister interface {
	// Conversations returns detached, unique identifiers in lexical order. An
	// empty store yields a non-nil empty slice; concurrent writes may appear or
	// not according to the backend's snapshot boundary.
	Conversations(ctx context.Context) ([]ConversationID, error)
}
