package openai

import (
	"fmt"
)

// newIgnoredOptionError reports an option the provider documents as discarded.
// Sending it and letting the provider drop it is indistinguishable, to the
// caller, from this adapter never having mapped it.
func newIgnoredOptionError(provider string, option ChatOption) error {
	return fmt.Errorf("openai: %s ignores %s, so setting it would have no effect", provider, option)
}
