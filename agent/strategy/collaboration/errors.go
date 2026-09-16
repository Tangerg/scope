package collaboration

import "errors"

var (
	ErrInvalidConfig   = errors.New("collaboration: invalid configuration")
	ErrInvalidDecision = errors.New("collaboration: invalid decision")
	ErrInvalidState    = errors.New("collaboration: invalid execution state")
	ErrInvalidProtocol = errors.New("collaboration: invalid execution protocol")
	ErrTurnLimit       = errors.New("collaboration: turn limit reached")
)
