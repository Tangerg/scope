package xai_test

import (
	"testing"

	"github.com/Tangerg/scope/models/xai"
)

func TestConstructorsRejectAnAbsentCredential(t *testing.T) {
	cases := map[string]func() error{
		"NewChatCompletions": func() error {
			_, err := xai.NewChatCompletions(t.Context(), xai.ChatCompletionsConfig{})
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
	if xai.Provider == "" {
		t.Fatal("Provider is empty")
	}
}
