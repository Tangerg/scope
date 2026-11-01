package workflow

import (
	"errors"

	"github.com/Tangerg/scope/agent/run"
)

// Construction sentinels reject caller data before execution and therefore
// carry no Failure classification.
var (
	ErrInvalidStage = errors.New("workflow: invalid stage")

	ErrInvalidDefinitionConfig = errors.New("workflow: invalid definition configuration")
)

// Execution sentinels own the Failure persisted when they reach Step. Wrapping
// one is the only way a Step error acquires its classification.
var (
	ErrInvalidExecutionState = run.NewClassifiedError(
		run.FailureKindContract,
		"workflow.state.invalid",
		"workflow: invalid execution state",
	)
	ErrInvalidProtocol = run.NewClassifiedError(
		run.FailureKindContract,
		"workflow.protocol.invalid",
		"workflow: invalid protocol payload",
	)
)
