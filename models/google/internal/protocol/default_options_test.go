package protocol

import (
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
)

// Defaults are the options of every request that overrides nothing, so a
// default the endpoint refuses must fail the binding, not every later call.
func TestChatConfigRefusesDefaultsNoRequestCouldSend(t *testing.T) {
	ownedField := corechat.Options{}
	if err := ownedField.Extensions.Set(RequestExtensionKey, map[string]any{"temperature": 0.5}); err != nil {
		t.Fatal(err)
	}
	for name, defaults := range map[string]corechat.Options{
		"reasoning effort": {ReasoningEffort: "max"},
		"token limit":      {MaxOutputTokens: new(int64(1) << 40)},
		"core-owned field": ownedField,
	} {
		config := ChatConfig{Provider: GoogleProvider, Client: ClientConfig{APIKey: "test"}, DefaultOptions: defaults}
		if err := config.Validate(); err == nil {
			t.Errorf("Validate accepted %s defaults", name)
		}
	}
}
