package coordination

import "errors"

var (
	ErrInvalidConfig   = errors.New("coordination: invalid configuration")
	ErrInvalidState    = errors.New("coordination: invalid execution state")
	ErrInvalidProtocol = errors.New("coordination: invalid execution protocol")
)
