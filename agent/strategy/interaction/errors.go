package interaction

import "errors"

// Invalid-value sentinels identify the boundary that rejected caller data.
// ErrInvalidResult covers public result validators; protocol and restored-state
// errors remain distinct because their recovery responsibilities differ.
var (
	ErrInvalidResult                = errors.New("interaction: invalid result")
	ErrInvalidSteer                 = errors.New("interaction: invalid steer")
	ErrInvalidToolInputRequest      = errors.New("interaction: invalid tool input request")
	ErrToolInputRequired            = errors.New("interaction: tool input required")
	ErrToolAdvertisementUnavailable = errors.New("interaction: tool advertisement unavailable")
	ErrInvalidToolAdvertisement     = errors.New("interaction: invalid tool advertisement")
	ErrInvalidPendingToolInput      = errors.New("interaction: invalid pending tool input")
	ErrInvalidDefinitionConfig      = errors.New("interaction: invalid definition configuration")
	ErrInvalidDispatcherConfig      = errors.New("interaction: invalid dispatcher configuration")
	ErrInvalidToolSet               = errors.New("interaction: invalid tool set")
	ErrInvalidDelegate              = errors.New("interaction: invalid delegate")
	ErrInvalidArtifact              = errors.New("interaction: invalid artifact")
	ErrInvalidInput                 = errors.New("interaction: invalid input")
	ErrInvalidExecutionState        = errors.New("interaction: invalid execution state")
	ErrInvalidProtocol              = errors.New("interaction: invalid protocol payload")
)

// ErrModelResponseTooLarge reports response resource admission failure.
// After model execution starts, the incomplete response is discarded and its
// Effect remains unknown. Oversize replacement context is rejected before calling
// the model and settles as a definite host failure.
var ErrModelResponseTooLarge = errors.New("interaction: model response exceeds byte limit")

// ErrHostFailure separates host infrastructure failure from model or tool behavior.
var ErrHostFailure = errors.New("interaction: host failure")
