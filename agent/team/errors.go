package team

import (
	"errors"

	"github.com/Tangerg/scope/agent/run"
)

// ErrInvalidConfig rejects construction before execution and therefore carries
// no Failure classification.
var ErrInvalidConfig = errors.New("team: invalid configuration")

// Execution sentinels own the Failure persisted when they reach Step. Wrapping
// one is the only way a Step error acquires its classification.
var (
	ErrInvalidDecision = run.NewClassifiedError(
		run.FailureKindContract,
		"team.decision.invalid",
		"team: invalid decision",
	)
	ErrInvalidExecutionState = run.NewClassifiedError(
		run.FailureKindContract,
		"team.state.invalid",
		"team: invalid execution state",
	)
	ErrInvalidProtocol = run.NewClassifiedError(
		run.FailureKindContract,
		"team.protocol.invalid",
		"team: invalid execution protocol",
	)
	ErrTurnLimit = run.NewClassifiedError(
		run.FailureKindExecution,
		"team.limit.turns",
		"team: turn limit reached",
	)
)
