package anthropic_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
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

type endpointTransport struct {
	url   string
	calls int
}

func (e *endpointTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	e.calls++
	e.url = r.URL.String()
	return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"captured"}}`)), Request: r}, nil
}

func TestChatCompletionsValidatesAndUsesAnthropicEndpoint(t *testing.T) {
	t.Setenv("OPENAI_BASE_URL", "https://unrelated.invalid/v1")
	transport := new(endpointTransport)
	cfg := anthropic.ChatCompletionsConfig{APIKey: "test", HTTPClient: &http.Client{Transport: transport}, BaseURL: "/relative"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate accepted an endpoint rejected by the actual constructor")
	}
	if _, err := anthropic.NewChatCompletions(t.Context(), cfg); err == nil {
		t.Fatal("constructed with relative endpoint")
	}
	cfg.BaseURL = ""
	model, err := anthropic.NewChatCompletions(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if transport.url != "" {
		t.Fatal("construction performed I/O")
	}
	req, err := corechat.NewRequest(corechat.NewUserMessage(corechat.NewTextPart("hello")))
	if err != nil {
		t.Fatal(err)
	}
	req.Options.Model = "test"
	if _, err = model.Call(t.Context(), req); err == nil {
		t.Fatal("captured native rejection unexpectedly succeeded")
	}
	if transport.url != "https://api.anthropic.com/v1/chat/completions" {
		t.Fatalf("Anthropic credentials would have been sent to %q", transport.url)
	}
}
