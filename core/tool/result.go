package tool

import (
	"errors"
	"fmt"

	"github.com/Tangerg/scope/core/chat"
)

var ErrInvalidResult = errors.New("tool: invalid result")

// Result is either a completed output or a request for external input. An input
// request carries no completed output. Errors describe execution failure, never
// suspension; the runtime alone owns committing and resuming a logical call.
// The zero result completes with an empty output.
type Result struct {
	Output       chat.ToolOutput
	InputRequest *InputRequest
}

func (r Result) Validate() error {
	if err := r.Output.Validate(); err != nil {
		return err
	}
	if r.InputRequest == nil {
		return nil
	}
	if !r.InputRequest.Valid() || len(r.Output.Content) != 0 || len(r.Output.Details) != 0 {
		return fmt.Errorf("%w: input request must be valid and exclude output", ErrInvalidResult)
	}
	return nil
}
