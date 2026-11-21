package anthropic

import (
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
)

// Defaults are the options of every request that overrides nothing, so a
// default the endpoint refuses must fail the binding, not every later call.
func TestNewMessagesRefusesDefaultsNoRequestCouldSend(t *testing.T) {
	ownedField := corechat.Options{}
	if err := ownedField.Extensions.Set(RequestExtensionKey, map[string]any{requestFieldStopSequences: []string{"END"}}); err != nil {
		t.Fatal(err)
	}
	for name, defaults := range map[string]corechat.Options{
		"penalty":          {FrequencyPenalty: new(0.5)},
		"sampling":         {Temperature: new(0.5)},
		"reasoning effort": {ReasoningEffort: "minimal"},
		"core-owned field": ownedField,
	} {
		config := MessagesConfig{APIKey: "test", DefaultOptions: defaults}
		if _, err := NewMessages(t.Context(), config, Dialect{Provider: Provider}); err == nil {
			t.Errorf("NewMessages accepted %s defaults", name)
		}
	}
}
