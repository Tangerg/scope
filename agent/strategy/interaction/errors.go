package interaction

import (
	"errors"

	agent "github.com/Tangerg/scope/agent"
)

// Invalid-value sentinels identify the boundary that rejected caller data.
// ErrInvalidResult covers public result validators. These reject construction,
// domain values, and Dispatcher operations rather than Steps, so they carry no
// Failure classification.
var (
	ErrInvalidResult                = errors.New("interaction: invalid result")
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
)

// Execution sentinels own the Failure persisted when they reach Step. Wrapping
// one is the only way a Step error acquires its classification. Protocol and
// restored-state errors stay distinct because their recovery responsibilities
// differ.
var (
	ErrInvalidSteer = agent.NewClassifiedError(
		agent.FailureKindContract,
		"interaction.signal.invalid",
		"interaction: invalid steer",
	)
	ErrInvalidExecutionState = agent.NewClassifiedError(
		agent.FailureKindContract,
		"interaction.state.invalid",
		"interaction: invalid execution state",
	)
	ErrInvalidProtocol = agent.NewClassifiedError(
		agent.FailureKindContract,
		"interaction.protocol.invalid",
		"interaction: invalid protocol payload",
	)
)

// ErrModelResponseTooLarge reports response resource admission failure.
// After model execution starts, the incomplete response is discarded and its
// Effect remains unknown. Oversize replacement context is rejected before calling
// the model and settles as a definite host failure.
var ErrModelResponseTooLarge = errors.New("interaction: model response exceeds byte limit")

// ErrHostFailure separates host infrastructure failure from model or tool behavior.
var ErrHostFailure = errors.New("interaction: host failure")
