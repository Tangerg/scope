package planning

import (
	"errors"

	agent "github.com/Tangerg/scope/agent"
)

// Invalid-value sentinels identify the boundary that rejected caller data.
// ErrInvalidResult covers public result validators; protocol and restored-state
// errors remain distinct because their recovery responsibilities differ.
// These reject construction and domain values before execution, so they carry
// no Failure classification.
var (
	ErrInvalidResult           = errors.New("planning: invalid result")
	ErrInvalidCondition        = errors.New("planning: invalid condition")
	ErrInvalidWorldState       = errors.New("planning: invalid world state")
	ErrInvalidGoal             = errors.New("planning: invalid goal")
	ErrInvalidAction           = errors.New("planning: invalid action")
	ErrInvalidActionCost       = errors.New("planning: invalid action cost")
	ErrInvalidPlan             = errors.New("planning: invalid plan")
	ErrInvalidProblem          = errors.New("planning: invalid problem")
	ErrInvalidDefinitionConfig = errors.New("planning: invalid definition configuration")
	ErrInvalidDispatcherConfig = errors.New("planning: invalid dispatcher configuration")
)

// Execution sentinels own the Failure persisted when they reach Step. Wrapping
// one is the only way a Step error acquires its classification.
var (
	ErrInvalidExecutionState = agent.NewClassifiedError(
		agent.FailureKindContract,
		"planning.state.invalid",
		"planning: invalid execution state",
	)
	ErrInvalidProtocol = agent.NewClassifiedError(
		agent.FailureKindContract,
		"planning.protocol.invalid",
		"planning: invalid protocol payload",
	)
)
