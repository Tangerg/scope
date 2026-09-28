package interaction

import (
	"fmt"

	"github.com/Tangerg/scope/core/chat"
)

// Input excludes executable Tools: the Deployment fixes their ToolSet authority.
type Input struct {
	Messages []chat.Message `json:"messages"`

	Options chat.Options `json:"options,omitzero"`
}

func (i Input) Validate() error {
	request := &chat.Request{Messages: i.Messages, Options: i.Options}
	if err := request.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	return nil
}
