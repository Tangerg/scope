package tool

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/internal/errscope"
)

var ErrInvalidInputRejection = errors.New("tool: invalid input rejection")

type InputRejectionConfig struct {
	Message string
	Cause   error
}

// InputRejection declares model-correctable input with an explicitly public
// message. Cause is diagnostic only and cannot supply a different rejection,
// execution outcome, or control signal through errors.Is or errors.As.
type InputRejection struct {
	message string
	cause   error
}

func NewInputRejection(config InputRejectionConfig) (*InputRejection, error) {
	rejection := &InputRejection{message: config.Message, cause: config.Cause}
	if err := rejection.Validate(); err != nil {
		return nil, err
	}
	return rejection, nil
}

func (i *InputRejection) Validate() error {
	if i == nil || strings.TrimSpace(i.message) == "" || !utf8.ValidString(i.message) {
		return fmt.Errorf("%w: message must be nonempty UTF-8", ErrInvalidInputRejection)
	}
	if i.cause != nil && lo.IsNil(i.cause) {
		return fmt.Errorf("%w: cause is typed nil", ErrInvalidInputRejection)
	}
	return nil
}

func (i *InputRejection) Error() string {
	if i == nil || i.message == "" {
		return ErrInvalidInputRejection.Error()
	}
	return i.message
}

func (i *InputRejection) Message() string {
	if i == nil {
		return ""
	}
	return i.message
}

func (i *InputRejection) Cause() error {
	if i == nil {
		return nil
	}
	return i.cause
}

func (i *InputRejection) Is(target error) bool {
	return i.Validate() == nil && target == ErrInvalidInvocation
}

// ClassifyInputRejection finds one explicit rejection through transparent
// wrapping. Joined independent errors require their owner to decide which
// message is public; private diagnostic causes are never inspected.
func ClassifyInputRejection(err error) (*InputRejection, bool) {
	rejection, found := errscope.Find[*InputRejection](err)
	if !found || rejection.Validate() != nil {
		return nil, false
	}
	return rejection, true
}

func rejectInput(message string, cause error) error {
	rejection, err := NewInputRejection(InputRejectionConfig{Message: message, Cause: cause})
	if err != nil {
		return &InputValidationError{cause: err}
	}
	return rejection
}
