package history

import (
	"context"
	"errors"

	"github.com/Tangerg/scope/core/chat"
)

var (
	ErrNilReader = errors.New("history: nil reader")
	ErrNilWriter = errors.New("history: nil writer")
)

type Reader interface {
	// Read returns a detached snapshot in stored order. Unknown and closed conversations
	// yield a non-nil empty slice; implementations honor ctx and never expose
	// mutable backing storage.
	Read(ctx context.Context, conversationID ConversationID) ([]chat.Message, error)
}

// Writer orders each complete batch atomically. Relative ordering of concurrent
// batches is implementation-defined; messages within a batch retain their order.
type Writer interface {
	// Append accepts one immutable batch under its stable identity. An identical
	// retry returns the original receipt without appending again; changed content
	// returns ErrAppendConflict. Errors without a valid receipt leave the outcome
	// unconfirmed and never authorize submitting under a new identity.
	// The closed-conversation check, deduplication record, and message append share
	// one atomic boundary. While the conversation is open, message expiration must
	// not erase deduplication evidence.
	Append(ctx context.Context, batch Batch) (Receipt, error)
}

type ReadWriter interface {
	Reader
	Writer
}

type Clearer interface {
	// Clear atomically closes this identity, including one not yet written. Closed
	// conversations read as empty and disappear from listing. Later appends,
	// including retries of accepted batches, return ErrConversationClosed.
	// Physical message cleanup can follow closure; Clear returns nil only when
	// cleanup completes. A cleanup error leaves the identity closed, and another
	// Clear retries cleanup. Closure evidence never expires; a new conversation
	// requires a new ConversationID. Permanent closure evidence can replace the
	// closed conversation's individual append receipts.
	Clear(ctx context.Context, conversationID ConversationID) error
}

type Store interface {
	ReadWriter
	Clearer
}

type Lister interface {
	// Conversations returns detached, unique identifiers in lexical order. An
	// empty store yields a non-nil empty slice; closed conversations are omitted.
	// Concurrent appends may appear according to the backend's snapshot boundary.
	Conversations(ctx context.Context) ([]ConversationID, error)
}
