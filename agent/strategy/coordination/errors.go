package coordination

import (
	"errors"

	"github.com/Tangerg/scope/agent"
)

var (
	ErrInvalidConfig   = errors.New("coordination: invalid configuration")
	ErrInvalidState    = errors.New("coordination: invalid execution state")
	ErrInvalidProtocol = errors.New("coordination: invalid execution protocol")
)

const failureCodeCoordinationProtocolInvalid = "coordination.protocol.invalid"

func protocolStepError(cause error) error {
	failure, err := agent.NewFailure(agent.FailureKindContract, failureCodeCoordinationProtocolInvalid, agent.NormalizeDiagnostic(cause.Error()))
	if err != nil {
		return err
	}
	return &agent.StepError{Failure: failure, Cause: cause}
}
