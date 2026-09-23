package collaboration

import (
	"errors"

	agent "github.com/Tangerg/scope/agent"
)

// ErrInvalidConfig rejects construction before execution and therefore carries
// no Failure classification.
var ErrInvalidConfig = errors.New("collaboration: invalid configuration")

// Execution sentinels own the Failure persisted when they reach Step. Wrapping
// one is the only way a Step error acquires its classification.
var (
	ErrInvalidDecision = agent.NewClassifiedError(
		agent.FailureKindContract,
		"collaboration.decision.invalid",
		"collaboration: invalid decision",
	)
	ErrInvalidExecutionState = agent.NewClassifiedError(
		agent.FailureKindContract,
		"collaboration.state.invalid",
		"collaboration: invalid execution state",
	)
	ErrInvalidProtocol = agent.NewClassifiedError(
		agent.FailureKindContract,
		"collaboration.protocol.invalid",
		"collaboration: invalid execution protocol",
	)
	ErrTurnLimit = agent.NewClassifiedError(
		agent.FailureKindExecution,
		"collaboration.limit.turns",
		"collaboration: turn limit reached",
	)
)
