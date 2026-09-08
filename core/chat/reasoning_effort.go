package chat

import (
	"errors"
	"strings"
)

// ReasoningEffort is a provider-neutral reasoning intensity selected from a
// model's advertised values. It is intentionally open rather than a fixed enum:
// the selected model owns its accepted vocabulary.
//
// Being open makes it the easiest option to drop by accident, because there is
// no enum to switch over and no compiler complaint for leaving it out. An
// adapter whose provider expresses reasoning as something other than a level —
// a token budget, say — cannot translate a level into it without inventing the
// number, so it reports the option as unsupported and lets the caller state the
// provider's own parameters through an extension. What it must not do is accept
// the effort and send a request that never carried it.
type ReasoningEffort string

// Validate rejects values whose identity would change under trimming. Empty is
// valid and asks the provider adapter to use the selected model's default.
func (r ReasoningEffort) Validate() error {
	if r != ReasoningEffort(strings.TrimSpace(string(r))) {
		return errors.New("reasoning effort must not have surrounding whitespace")
	}
	return nil
}
