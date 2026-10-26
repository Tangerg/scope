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
