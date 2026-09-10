package planning

import "errors"

var (
	ErrInvalidCondition        = errors.New("planning: invalid condition")
	ErrInvalidWorldState       = errors.New("planning: invalid world state")
	ErrInvalidGoal             = errors.New("planning: invalid goal")
	ErrInvalidAction           = errors.New("planning: invalid action")
	ErrInvalidActionCost       = errors.New("planning: invalid action cost")
	ErrInvalidPlan             = errors.New("planning: invalid plan")
	ErrInvalidProblem          = errors.New("planning: invalid problem")
	ErrInvalidDefinitionConfig = errors.New("planning: invalid definition configuration")
	ErrInvalidDispatcherConfig = errors.New("planning: invalid dispatcher configuration")
	ErrInvalidExecutionState   = errors.New("planning: invalid execution state")
	ErrInvalidProtocol         = errors.New("planning: invalid protocol payload")
)
