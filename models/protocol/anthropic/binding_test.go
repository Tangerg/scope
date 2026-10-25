package anthropic_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/protocol/anthropic"
)

type bindingTransport struct {
	calls      int
	requestURL string
}

func (b *bindingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	b.calls++
	b.requestURL = request.URL.String()
	return nil, errors.New("unexpected construction HTTP I/O")
}

func TestConstructorsValidateHTTPBindingWithoutIO(t *testing.T) {
	constructors := map[string]func(string, *http.Client) (bool, error){
		"NewMessages": func(endpoint string, client *http.Client) (bool, error) {
			model, err := anthropic.NewMessages(t.Context(), anthropic.MessagesConfig{APIKey: "test-key", BaseURL: endpoint, HTTPClient: client})
			return model == nil, err
		},
		"NewTextCounter": func(endpoint string, client *http.Client) (bool, error) {
			model, err := anthropic.NewTextCounter(t.Context(), anthropic.TextCounterConfig{APIKey: "test-key", BaseURL: endpoint, HTTPClient: client, Model: "test-model"})
			return model == nil, err
		},
		"NewCompatibleMessages": func(endpoint string, client *http.Client) (bool, error) {
			model, err := anthropic.NewCompatibleMessages(t.Context(), anthropic.MessagesConfig{APIKey: "test-key", BaseURL: endpoint, HTTPClient: client}, anthropic.Dialect{Provider: "compatible"})
			return model == nil, err
		},
	}
	for name, construct := range constructors {
		t.Run(name, func(t *testing.T) {
			transport := &bindingTransport{}
			client := &http.Client{Transport: transport}
			for _, endpoint := range []string{"%", "/relative", "//example.test/path", "ftp://example.test", "https://", "https://:443", "https://example.test:bad", "https://example.test/\x00", "https://user:pass@example.test/path"} {
				missing, err := construct(endpoint, client)
				if err == nil || !missing {
					t.Errorf("BaseURL %q: nil model = %t, error = %v", endpoint, missing, err)
				}
			}
			for _, endpoint := range []string{"", "http://localhost:1234/proxy/prefix", "https://example.test/path?key=value", "https://example.test/path#section"} {
				missing, err := construct(endpoint, client)
				if err != nil || missing {
					t.Errorf("BaseURL %q: nil model = %t, error = %v", endpoint, missing, err)
				}
			}
			if transport.calls != 0 {
				t.Fatalf("construction made %d HTTP calls", transport.calls)
			}
		})
	}
}

func TestBindingValidatesSelectedEnvironmentURLBeforeIO(t *testing.T) {
	for _, environmentURL := range []string{"%", "", "/relative", "ftp://example.test"} {
		t.Run(environmentURL, func(t *testing.T) {
			t.Setenv("ANTHROPIC_BASE_URL", environmentURL)
			transport := &bindingTransport{}
			for _, explicitURL := range []string{"", "https://explicit.example.test"} {
				model, err := anthropic.NewMessages(t.Context(), anthropic.MessagesConfig{APIKey: "test-key", BaseURL: explicitURL, HTTPClient: &http.Client{Transport: transport}})
				if explicitURL == "" {
					if err == nil || model != nil || !strings.Contains(err.Error(), "ANTHROPIC_BASE_URL") {
						t.Fatalf("environment binding: model = %v, error = %v", model, err)
					}
				} else if err != nil || model == nil {
					t.Fatalf("explicit binding: model = %v, error = %v", model, err)
				}
			}
			if transport.calls != 0 {
				t.Fatal("invalid environment binding performed HTTP I/O")
			}
		})
	}
}

func TestBindingCallsExplicitEndpointWithInvalidEnvironmentURL(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "%")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "environment-token")
	t.Setenv("ANTHROPIC_CUSTOM_HEADERS", "X-Environment: must-not-be-sent")
	headers := http.Header{"X-Binding": []string{"frozen"}}
	var calls atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Method != http.MethodPost || request.URL.Path != "/explicit/v1/messages" {
			t.Errorf("request = %s %s", request.Method, request.URL)
		}
		for name, want := range map[string]string{
			"X-Api-Key": "test-key", "Anthropic-Version": "2023-06-01", "X-Binding": "frozen",
			"Authorization": "", "X-Environment": "",
		} {
			if got := request.Header.Get(name); got != want {
				t.Errorf("%s = %q; want %q", name, got, want)
			}
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		writeAnthropicBehaviorStart(writer)
		writeAnthropicBehaviorEvent(writer, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ready"}}`)
		writeAnthropicBehaviorEvent(writer, "content_block_stop", `{"type":"content_block_stop","index":0}`)
		writeAnthropicBehaviorEvent(writer, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}`)
		writeAnthropicBehaviorEvent(writer, "message_stop", `{"type":"message_stop"}`)
	}))
	defer server.Close()
	model, err := anthropic.NewMessages(t.Context(), anthropic.MessagesConfig{
		APIKey: "test-key", BaseURL: server.URL + "/explicit", HTTPClient: server.Client(), Headers: headers,
		DefaultOptions: corechat.Options{Model: "claude-test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	headers.Set("X-Binding", "mutated")
	t.Setenv("ANTHROPIC_BASE_URL", "/another-invalid-value")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "changed-environment-token")
	response, err := model.Call(t.Context(), &corechat.Request{
		Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Output == nil || response.Output.Message == nil || len(response.Output.Message.Parts) != 1 || response.Output.Message.Parts[0].Text != "ready" {
		t.Fatalf("response = %#v", response)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("HTTP calls = %d; want 1", got)
	}
}

func TestBindingPreservesHTTPClientRedirectPolicy(t *testing.T) {
	var calls, redirects atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.URL.Path == "/v1/models" {
			http.Redirect(writer, request, "/redirected", http.StatusTemporaryRedirect)
			return
		}
		t.Error("HTTP client redirect policy was bypassed")
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"data":[],"has_more":false}`)
	}))
	defer server.Close()
	client := server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		redirects.Add(1)
		return http.ErrUseLastResponse
	}
	model, err := anthropic.NewMessages(t.Context(), anthropic.MessagesConfig{
		APIKey: "test-key", BaseURL: server.URL, HTTPClient: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, listErr := model.ListModels(t.Context(), 1, 1024); listErr == nil {
		t.Fatal("rejected redirect became a successful model listing")
	}
	if calls.Load() != 1 || redirects.Load() != 1 {
		t.Fatalf("HTTP calls = %d, redirects = %d; want 1 each", calls.Load(), redirects.Load())
	}
}

func TestBindingFreezesEnvironmentURLAndPreservesExplicitPrecedence(t *testing.T) {
	paths := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		paths <- request.URL.Path
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"data":[],"has_more":false}`)
	}))
	defer server.Close()
	for _, test := range []struct{ explicitURL, wantPath string }{
		{"", "/environment/v1/models"},
		{server.URL + "/explicit", "/explicit/v1/models"},
	} {
		t.Setenv("ANTHROPIC_BASE_URL", server.URL+"/environment")
		model, err := anthropic.NewMessages(t.Context(), anthropic.MessagesConfig{APIKey: "test-key", BaseURL: test.explicitURL, HTTPClient: server.Client()})
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("ANTHROPIC_BASE_URL", "%")
		if _, listErr := model.ListModels(t.Context(), 1, 1024); listErr != nil {
			t.Fatal(listErr)
		}
		if got := <-paths; got != test.wantPath {
			t.Fatalf("path = %q; want %q", got, test.wantPath)
		}
	}
}

func TestBindingUsesSDKDefaultWhenEnvironmentIsAbsent(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "restored by cleanup")
	if err := os.Unsetenv("ANTHROPIC_BASE_URL"); err != nil {
		t.Fatal(err)
	}
	transport := &bindingTransport{}
	model, err := anthropic.NewMessages(t.Context(), anthropic.MessagesConfig{APIKey: "test-key", HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	if transport.calls != 0 {
		t.Fatal("default binding performed construction HTTP I/O")
	}
	if _, listErr := model.ListModels(t.Context(), 1, 1024); listErr == nil {
		t.Fatal("injected transport failure was lost")
	}
	if transport.requestURL != "https://api.anthropic.com/v1/models?limit=1" || transport.calls != 1 {
		t.Fatalf("default endpoint = %q, calls = %d", transport.requestURL, transport.calls)
	}
}
