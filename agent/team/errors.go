package team

import (
	"errors"

	agentrun "github.com/Tangerg/scope/agent/run"
)

// ErrInvalidConfig rejects construction before execution and therefore carries
// no Failure classification.
var ErrInvalidConfig = errors.New("collaboration: invalid configuration")

// Execution sentinels own the Failure persisted when they reach Step. Wrapping
// one is the only way a Step error acquires its classification.
var (
	ErrInvalidDecision = agentrun.NewClassifiedError(
		agentrun.FailureKindContract,
		"collaboration.decision.invalid",
		"collaboration: invalid decision",
	)
	ErrInvalidExecutionState = agentrun.NewClassifiedError(
		agentrun.FailureKindContract,
		"collaboration.state.invalid",
		"collaboration: invalid execution state",
	)
	ErrInvalidProtocol = agentrun.NewClassifiedError(
		agentrun.FailureKindContract,
		"collaboration.protocol.invalid",
		"collaboration: invalid execution protocol",
	)
	ErrTurnLimit = agentrun.NewClassifiedError(
		agentrun.FailureKindExecution,
		"collaboration.limit.turns",
		"collaboration: turn limit reached",
	)
)
