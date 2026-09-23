package coordination

import (
	"errors"

	"github.com/Tangerg/scope/agent"
)

// ErrInvalidConfig rejects construction before execution and therefore carries
// no Failure classification.
var ErrInvalidConfig = errors.New("coordination: invalid configuration")

// Execution sentinels own the Failure persisted when they reach Step. Wrapping
// one is the only way a Step error acquires its classification.
var (
	ErrInvalidExecutionState = agent.NewClassifiedError(
		agent.FailureKindContract,
		"coordination.state.invalid",
		"coordination: invalid execution state",
	)
	ErrInvalidProtocol = agent.NewClassifiedError(
		agent.FailureKindContract,
		"coordination.protocol.invalid",
		"coordination: invalid execution protocol",
	)
)
