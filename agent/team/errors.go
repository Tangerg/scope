package team

import (
	"errors"

	agentrun "github.com/Tangerg/scope/agent/run"
)

// ErrInvalidConfig rejects construction before execution and therefore carries
// no Failure classification.
var ErrInvalidConfig = errors.New("team: invalid configuration")

// Execution sentinels own the Failure persisted when they reach Step. Wrapping
// one is the only way a Step error acquires its classification.
var (
	ErrInvalidDecision = agentrun.NewClassifiedError(
		agentrun.FailureKindContract,
		"team.decision.invalid",
		"team: invalid decision",
	)
	ErrInvalidExecutionState = agentrun.NewClassifiedError(
		agentrun.FailureKindContract,
		"team.state.invalid",
		"team: invalid execution state",
	)
	ErrInvalidProtocol = agentrun.NewClassifiedError(
		agentrun.FailureKindContract,
		"team.protocol.invalid",
		"team: invalid execution protocol",
	)
	ErrTurnLimit = agentrun.NewClassifiedError(
		agentrun.FailureKindExecution,
		"team.limit.turns",
		"team: turn limit reached",
	)
)
