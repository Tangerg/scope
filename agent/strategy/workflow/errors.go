package workflow

import "errors"

var (
	ErrInvalidStage = errors.New("workflow: invalid stage")

	ErrInvalidDefinitionConfig = errors.New("workflow: invalid definition configuration")

	ErrInvalidExecutionState = errors.New("workflow: invalid execution state")

	ErrInvalidProtocol = errors.New("workflow: invalid protocol payload")
)
