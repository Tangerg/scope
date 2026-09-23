package interaction

import (
	"context"

	"github.com/Tangerg/scope/core/chat"
)

// ModelContextReducer owns an optional, provider-neutral reduction immediately
// before one actual model call. The request is an independently owned snapshot
// containing the exact Tool manifest and options that the model would receive;
// implementations may inspect it but return only the complete replacement
// message sequence, so they cannot change model options or Tool authority.
// The settlement carries a replacement only when those messages changed.
// Once consumed, WorkingContext owns the replacement and the mailbox retains
// only the Signal's content digest and runtime routing facts.
//
// ReduceModelContext must return a definite outcome. A non-nil error means the
// main model was not called and is settled as a Host failure. Implementations
// that perform I/O must therefore resolve their own ambiguity before returning.
type ModelContextReducer interface {
	// ReduceModelContext returns the complete messages for the attributed model
	// invocation. The result must be non-empty and must satisfy request
	// validation with those messages installed; a rejected result settles as a
	// Host failure. The Dispatcher clones the result before using it, so
	// implementations may return a sequence they still reference.
	ReduceModelContext(
		ctx context.Context,
		invocation ModelInvocation,
		request *chat.Request,
	) ([]chat.Message, error)
}
