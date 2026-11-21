package mistral

import (
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
)

// Defaults are the options of every request that overrides nothing, so a
// default Mistral refuses must fail the binding, not every later call.
func TestChatConfigRefusesDefaultsNoRequestCouldSend(t *testing.T) {
	for name, defaults := range map[string]corechat.Options{
		"temperature":      {Temperature: new(1.6)},
		"reasoning effort": {ReasoningEffort: "max"},
	} {
		if err := (ChatConfig{APIKey: "test", DefaultOptions: defaults}).Validate(); err == nil {
			t.Errorf("Validate accepted %s defaults", name)
		}
	}
}
