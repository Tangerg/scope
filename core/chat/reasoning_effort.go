package chat

import (
	"errors"
	"strings"
)

// ReasoningEffort uses the selected model's vocabulary, not a fixed enum.
// Adapters must reject levels they cannot express; mapping a level to a token
// budget would invent caller policy. Provider-specific controls use extensions.
type ReasoningEffort string

// Validate rejects values whose identity would change under trimming. Empty is
// valid and asks the provider adapter to use the selected model's default.
func (r ReasoningEffort) Validate() error {
	if r != ReasoningEffort(strings.TrimSpace(string(r))) {
		return errors.New("reasoning effort must not have surrounding whitespace")
	}
	return nil
}
