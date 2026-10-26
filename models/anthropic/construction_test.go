package anthropic_test

import (
	"testing"

	"github.com/Tangerg/scope/models/anthropic"
)

func TestConstructorsRejectAnAbsentCredential(t *testing.T) {
	cases := map[string]func() error{
		"NewMessages": func() error {
			_, err := anthropic.NewMessages(t.Context(), anthropic.MessagesConfig{})
			return err
		},
		"NewChatCompletions": func() error {
			_, err := anthropic.NewChatCompletions(t.Context(), anthropic.ChatCompletionsConfig{})
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
	if anthropic.Provider == "" {
		t.Fatal("Provider is empty")
	}
}
