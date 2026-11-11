package planning

import (
	"errors"

	"github.com/Tangerg/scope/agent/run"
)

// Invalid-value sentinels reject construction and domain values before
// execution, so they carry no Failure classification.
var (
	ErrInvalidResult     = errors.New("planning: invalid result")
	ErrInvalidCondition  = errors.New("planning: invalid condition")
	ErrInvalidWorldState = errors.New("planning: invalid world state")
	ErrInvalidGoal       = errors.New("planning: invalid goal")
	ErrInvalidAction     = errors.New("planning: invalid action")
	ErrInvalidActionCost = errors.New("planning: invalid action cost")
	ErrInvalidPlan       = errors.New("planning: invalid plan")
	ErrInvalidProblem    = errors.New("planning: invalid problem")
	ErrInvalidConfig     = errors.New("planning: invalid configuration")
)

// Execution sentinels own the Failure persisted when they reach Step. Wrapping
// one is the only way a Step error acquires its classification. Protocol and
// restored-state errors stay distinct because their recovery responsibilities
// differ.
var (
	ErrInvalidExecutionState = run.NewClassifiedError(
		run.FailureKindContract,
		"planning.state.invalid",
		"planning: invalid execution state",
	)
	ErrInvalidProtocol = run.NewClassifiedError(
		run.FailureKindContract,
		"planning.protocol.invalid",
		"planning: invalid protocol payload",
	)
)
