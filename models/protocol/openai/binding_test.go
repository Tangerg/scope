package openai_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/moderation"
	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/protocol/openai"
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
		"NewChatCompletions": func(endpoint string, client *http.Client) (bool, error) {
			model, err := openai.NewChatCompletions(t.Context(), openai.ChatCompletionsConfig{APIKey: "test-key", BaseURL: endpoint, HTTPClient: client})
			return model == nil, err
		},
		"NewResponses": func(endpoint string, client *http.Client) (bool, error) {
			model, err := openai.NewResponses(t.Context(), openai.ResponsesConfig{APIKey: "test-key", BaseURL: endpoint, HTTPClient: client})
			return model == nil, err
		},
		"NewEmbeddingModel": func(endpoint string, client *http.Client) (bool, error) {
			model, err := openai.NewEmbeddingModel(t.Context(), openai.EmbeddingModelConfig{DefaultOptions: embedding.Options{Model: "test-model"}, APIKey: "test-key", BaseURL: endpoint, HTTPClient: client, Provider: "openai"})
			return model == nil, err
		},
		"NewImageModel": func(endpoint string, client *http.Client) (bool, error) {
			model, err := openai.NewImageModel(t.Context(), openai.ImageModelConfig{DefaultOptions: image.Options{Model: "test-model"}, APIKey: "test-key", BaseURL: endpoint, HTTPClient: client, Provider: "openai"})
			return model == nil, err
		},
		"NewModerationModel": func(endpoint string, client *http.Client) (bool, error) {
			model, err := openai.NewModerationModel(t.Context(), openai.ModerationModelConfig{DefaultOptions: moderation.Options{Model: "test-model"}, APIKey: "test-key", BaseURL: endpoint, HTTPClient: client, Provider: "openai"})
			return model == nil, err
		},
		"NewSpeechModel": func(endpoint string, client *http.Client) (bool, error) {
			model, err := openai.NewSpeechModel(t.Context(), openai.SpeechModelConfig{DefaultOptions: speech.Options{Model: "test-model"}, APIKey: "test-key", BaseURL: endpoint, HTTPClient: client, Provider: "openai"})
			return model == nil, err
		},
		"NewTranscriptionModel": func(endpoint string, client *http.Client) (bool, error) {
			model, err := openai.NewTranscriptionModel(t.Context(), openai.TranscriptionModelConfig{DefaultOptions: transcription.Options{Model: "test-model"}, APIKey: "test-key", BaseURL: endpoint, HTTPClient: client, Provider: "openai"})
			return model == nil, err
		},
		"NewAudioTranslationModel": func(endpoint string, client *http.Client) (bool, error) {
			model, err := openai.NewAudioTranslationModel(t.Context(), openai.AudioTranslationModelConfig{DefaultOptions: transcription.Options{Model: "test-model"}, APIKey: "test-key", BaseURL: endpoint, HTTPClient: client, Provider: "openai"})
			return model == nil, err
		},
		"NewCompatibleChatCompletions": func(endpoint string, client *http.Client) (bool, error) {
			model, err := openai.NewCompatibleChatCompletions(t.Context(), openai.ChatCompletionsConfig{APIKey: "test-key", BaseURL: endpoint, HTTPClient: client}, openai.Dialect{Provider: "deepseek", TokenLimitField: openai.TokenLimitMaxTokens})
			return model == nil, err
		},
	}
	for name, construct := range constructors {
		t.Run(name, func(t *testing.T) {
			transport := &bindingTransport{}
			client := &http.Client{Transport: transport}
			for _, endpoint := range []string{"%", "/relative", "//example.test/path", "ftp://example.test", "https://", "https://:443", "https://example.test:bad", "https://example.test/\x00"} {
				missing, err := construct(endpoint, client)
				if err == nil || !missing {
					t.Errorf("BaseURL %q: nil model = %t, error = %v", endpoint, missing, err)
				}
			}
			for _, endpoint := range []string{"", "http://localhost:1234/proxy/prefix", "https://example.test/path?key=value", "https://user:pass@example.test/path#section"} {
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

func TestBindingValidatesEnvironmentURLBeforeIO(t *testing.T) {
	for _, environmentURL := range []string{"%", "", "/relative", "ftp://example.test"} {
		t.Run(environmentURL, func(t *testing.T) {
			t.Setenv("OPENAI_BASE_URL", environmentURL)
			transport := &bindingTransport{}
			for _, explicitURL := range []string{"", "https://explicit.example.test"} {
				model, err := openai.NewChatCompletions(t.Context(), openai.ChatCompletionsConfig{APIKey: "test-key", BaseURL: explicitURL, HTTPClient: &http.Client{Transport: transport}})
				if err == nil || model != nil || !strings.Contains(err.Error(), "OPENAI_BASE_URL") {
					t.Fatalf("environment binding: model = %v, error = %v", model, err)
				}
			}
			if transport.calls != 0 {
				t.Fatal("invalid environment binding performed HTTP I/O")
			}
		})
	}
}

func TestBindingFreezesEnvironmentURLAndPreservesExplicitPrecedence(t *testing.T) {
	paths := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		paths <- request.URL.Path
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"data":[]}`)
	}))
	defer server.Close()
	for _, test := range []struct{ explicitURL, wantPath string }{
		{"", "/environment/models"},
		{server.URL + "/explicit", "/explicit/models"},
	} {
		t.Setenv("OPENAI_BASE_URL", server.URL+"/environment")
		model, err := openai.NewChatCompletions(t.Context(), openai.ChatCompletionsConfig{APIKey: "test-key", BaseURL: test.explicitURL, HTTPClient: server.Client()})
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("OPENAI_BASE_URL", "%")
		if _, listErr := model.ListModels(t.Context(), 1, 1024); listErr != nil {
			t.Fatal(listErr)
		}
		if got := <-paths; got != test.wantPath {
			t.Fatalf("path = %q; want %q", got, test.wantPath)
		}
	}
}

func TestBindingUsesSDKDefaultWhenEnvironmentIsAbsent(t *testing.T) {
	t.Setenv("OPENAI_BASE_URL", "restored by cleanup")
	if err := os.Unsetenv("OPENAI_BASE_URL"); err != nil {
		t.Fatal(err)
	}
	transport := &bindingTransport{}
	model, err := openai.NewChatCompletions(t.Context(), openai.ChatCompletionsConfig{APIKey: "test-key", HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	if transport.calls != 0 {
		t.Fatal("default binding performed construction HTTP I/O")
	}
	if _, listErr := model.ListModels(t.Context(), 1, 1024); listErr == nil {
		t.Fatal("injected transport failure was lost")
	}
	if transport.requestURL != "https://api.openai.com/v1/models" || transport.calls != 1 {
		t.Fatalf("default endpoint = %q, calls = %d", transport.requestURL, transport.calls)
	}
}
