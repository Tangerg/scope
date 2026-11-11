package fireworks_test

import (
	"testing"

	"github.com/Tangerg/scope/models/fireworks"
)

func TestConstructorsRejectAnAbsentCredential(t *testing.T) {
	cases := map[string]func() error{
		"NewChatCompletions": func() error {
			_, err := fireworks.NewChatCompletions(t.Context(), fireworks.ChatCompletionsConfig{})
			return err
		},
	}
	for name, construct := range cases {
		t.Run(name, func(t *testing.T) {
			if err := construct(); err == nil {
				t.Fatal("an empty config constructed a usable model")
			}
		})
	}
}

func TestProviderIdentifierIsPresent(t *testing.T) {
	if fireworks.Provider == "" {
		t.Fatal("Provider is empty")
	}
}

// Validate is the same admission NewChatCompletions applies, so a config it
// accepts cannot fail construction on an endpoint it never checked.
func TestValidateAdmitsOnlyWhatConstructionAccepts(t *testing.T) {
	config := fireworks.ChatCompletionsConfig{APIKey: "key", BaseURL: "ftp://api.fireworks.example/v1"}
	if err := config.Validate(); err == nil {
		t.Fatal("Validate accepted an endpoint that construction rejects")
	}
	if _, err := fireworks.NewChatCompletions(t.Context(), config); err == nil {
		t.Fatal("construction accepted a non-HTTP endpoint")
	}
}
