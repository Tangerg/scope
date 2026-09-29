package interaction

import (
	"context"

	"github.com/Tangerg/scope/core/chat"
)

// ModelContextReducer owns an optional, provider-neutral reduction immediately
// before one actual model call. The request is an independently owned snapshot
// containing the exact Tool manifest and options that the model would receive;
// implementations return only the complete replacement message sequence, so
// they cannot change model options or Tool authority. When the messages
// change, the settlement carries them and WorkingContext adopts them, so later
// calls and checkpoints never regrow the reduced context.
//
// ReduceModelContext must return a definite outcome. A non-nil error means the
// main model was not called and is settled as a Host failure. Implementations
// that perform I/O must therefore resolve their own ambiguity before returning.
type ModelContextReducer interface {
	// ReduceModelContext returns the complete, non-empty messages for the
	// attributed invocation; a result that fails request validation settles
	// as a Host failure. The Dispatcher clones the result, so implementations
	// may return a sequence they still reference.
	ReduceModelContext(
		ctx context.Context,
		invocation ModelInvocation,
		request *chat.Request,
	) ([]chat.Message, error)
}
