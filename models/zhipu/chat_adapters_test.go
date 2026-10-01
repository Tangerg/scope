package zhipu

import (
	"testing"

	"github.com/Tangerg/scope/core/chat"
)

func TestChatConfigsValidateCredentialAndOptions(t *testing.T) {
	t.Parallel()

	invalidOptions := chat.Options{Stop: []string{""}}
	tests := []struct {
		name string
		err  error
	}{
		{name: "credential", err: (ChatCompletionsConfig{}).Validate()},
		{name: "options", err: (ChatCompletionsConfig{APIKey: "key", DefaultOptions: invalidOptions}).Validate()},
		{name: "messages options", err: (MessagesConfig{APIKey: "key", DefaultOptions: invalidOptions}).Validate()},
	}
	for _, test := range tests {
		if test.err == nil {
			t.Fatalf("%s validation error = nil", test.name)
		}
	}
}

func TestChatConstructorsProduceProtocolAdapters(t *testing.T) {
	t.Parallel()

	model, err := NewChatCompletions(t.Context(), ChatCompletionsConfig{APIKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	if model == nil {
		t.Fatal("NewChatCompletions(t.Context(), ) = nil")
	}

	_, invalidErr := NewChatCompletions(t.Context(), ChatCompletionsConfig{})
	if invalidErr == nil {
		t.Fatal("NewChatCompletions(t.Context(), invalid config) error = nil")
	}

	anthropicModel, err := NewMessages(t.Context(), MessagesConfig{APIKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	if anthropicModel == nil {
		t.Fatal("NewMessages(t.Context(), ) = nil")
	}
	_, invalidErr = NewMessages(t.Context(), MessagesConfig{})
	if invalidErr == nil {
		t.Fatal("NewMessages(t.Context(), invalid config) error = nil")
	}
}
