package interaction

import (
	"errors"

	"github.com/Tangerg/scope/agent/run"
)

// These sentinels belong to construction, domain values, dispatcher operations,
// and Tool control flow rather than Steps, so they carry no Failure
// classification.
var (
	ErrInvalidResult            = errors.New("interaction: invalid result")
	ErrInvalidModelSettlement   = errors.New("interaction: invalid model settlement")
	ErrInvalidToolAdvertisement = errors.New("interaction: invalid tool advertisement")
	ErrInvalidPendingToolInput  = errors.New("interaction: invalid pending tool input")
	ErrInvalidDefinitionConfig  = errors.New("interaction: invalid definition configuration")
	ErrInvalidDispatcherConfig  = errors.New("interaction: invalid dispatcher configuration")
	ErrInvalidToolSet           = errors.New("interaction: invalid tool set")
	ErrInvalidDelegate          = errors.New("interaction: invalid delegate")
	ErrInvalidInput             = errors.New("interaction: invalid input")
)

// Execution sentinels own the Failure persisted when they reach Step. Wrapping
// one is the only way a Step error acquires its classification. Protocol and
// restored-state errors stay distinct because their recovery responsibilities
// differ.
var (
	ErrInvalidSteer = run.NewClassifiedError(
		run.FailureKindContract,
		"interaction.signal.invalid",
		"interaction: invalid steer",
	)
	ErrInvalidExecutionState = run.NewClassifiedError(
		run.FailureKindContract,
		"interaction.state.invalid",
		"interaction: invalid execution state",
	)
	ErrInvalidProtocol = run.NewClassifiedError(
		run.FailureKindContract,
		"interaction.protocol.invalid",
		"interaction: invalid protocol payload",
	)
	// ErrInvalidModelResponse rejects the whole Step, so a structurally valid
	// response that violates Interaction constraints never enters its state.
	ErrInvalidModelResponse = run.NewClassifiedError(
		run.FailureKindExternal,
		"interaction.model.invalid_response",
		"interaction: invalid model response",
	)
)

// ErrModelResponseTooLarge rejects a canonical settlement that exceeds its
// budget. Complete generation remains observable, but its Effect stays unknown.
// Oversize replacement context fails before the model is called.
var ErrModelResponseTooLarge = errors.New("interaction: model response exceeds byte limit")

// ErrModelStreamTooLarge stops consumption before an encoded delta exceeds the
// stream admission budget. Observed generation cannot establish non-execution.
var ErrModelStreamTooLarge = errors.New("interaction: encoded model response deltas exceed stream byte limit")
