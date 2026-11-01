package providerconformance_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/httpclient"
	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/rerank"
	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/anthropic"
	"github.com/Tangerg/scope/models/blackforestlabs"
	"github.com/Tangerg/scope/models/cohere"
	"github.com/Tangerg/scope/models/elevenlabs"
	"github.com/Tangerg/scope/models/gladia"
	"github.com/Tangerg/scope/models/google"
	"github.com/Tangerg/scope/models/google/vertexai"
	"github.com/Tangerg/scope/models/hume"
	"github.com/Tangerg/scope/models/jina"
	"github.com/Tangerg/scope/models/mistral"
	"github.com/Tangerg/scope/models/nomic"
	"github.com/Tangerg/scope/models/openai"
	"github.com/Tangerg/scope/models/openrouter"
	"github.com/Tangerg/scope/models/voyage"
	"github.com/Tangerg/scope/models/xiaomi"
)

type modelOriginCall struct {
	name     string
	header   string // when fixed by this binding rather than its external SDK
	noAPIKey bool
	call     func(context.Context, string, *http.Client) error
}

func modelOriginCalls() []modelOriginCall {
	return []modelOriginCall{
		{name: "openai", header: "Authorization", call: func(ctx context.Context, endpoint string, client *http.Client) error {
			model, err := openai.NewChatCompletions(ctx, openai.ChatCompletionsConfig{APIKey: "sentinel", BaseURL: endpoint, HTTPClient: client})
			if err != nil {
				return err
			}
			_, err = model.ListModels(ctx, 1, 1024)
			return err
		}},
		{name: "anthropic", call: func(ctx context.Context, endpoint string, client *http.Client) error {
			model, err := anthropic.NewMessages(ctx, anthropic.MessagesConfig{APIKey: "sentinel", BaseURL: endpoint, HTTPClient: client})
			if err != nil {
				return err
			}
			_, err = model.ListModels(ctx, 1, 1024)
			return err
		}},
		{name: "xiaomi", header: "api-key", call: func(ctx context.Context, endpoint string, client *http.Client) error {
			model, err := xiaomi.NewMessages(ctx, xiaomi.MessagesConfig{APIKey: "sentinel", BaseURL: endpoint, HTTPClient: client})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}, Options: chat.Options{Model: "test"}})
			return err
		}},
		{name: "openrouter messages", header: "Authorization", call: func(ctx context.Context, endpoint string, client *http.Client) error {
			model, err := openrouter.NewMessages(ctx, openrouter.MessagesConfig{APIKey: "sentinel", BaseURL: endpoint, HTTPClient: client})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}, Options: chat.Options{Model: "test"}})
			return err
		}},
		{name: "google generation", header: "x-goog-api-key", call: func(ctx context.Context, endpoint string, client *http.Client) error {
			model, err := google.NewChat(ctx, google.ChatConfig{APIKey: "sentinel", BaseURL: endpoint, HTTPClient: client, DefaultOptions: chat.Options{Model: "test"}})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}})
			return err
		}},
		{name: "google interactions", header: "x-goog-api-key", call: func(ctx context.Context, endpoint string, client *http.Client) error {
			model, err := google.NewImageModel(ctx, google.ImageModelConfig{APIKey: "sentinel", BaseURL: endpoint, HTTPClient: client, DefaultOptions: image.Options{Model: google.ModelGemini31FlashImage}})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &image.Request{Prompt: "image"})
			return err
		}},
		{name: "vertex image", noAPIKey: true, call: func(ctx context.Context, endpoint string, client *http.Client) error {
			model, err := vertexai.NewImageModel(ctx, vertexai.ImageModelConfig{
				Client:         vertexai.ClientConfig{Project: "project", Location: "global", BaseURL: endpoint, HTTPClient: client},
				DefaultOptions: image.Options{Model: vertexai.ModelGemini3ProImage},
			})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &image.Request{Prompt: "image"})
			return err
		}},
		{name: "blackforestlabs", header: "x-key", call: func(ctx context.Context, endpoint string, client *http.Client) error {
			model, err := blackforestlabs.NewImageModel(ctx, blackforestlabs.ImageModelConfig{APIKey: "sentinel", BaseURL: endpoint, HTTPClient: client, DefaultOptions: image.Options{Model: blackforestlabs.ModelFlux11Pro}})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &image.Request{Prompt: "image"})
			return err
		}},
		{name: "elevenlabs", header: "xi-api-key", call: func(ctx context.Context, endpoint string, client *http.Client) error {
			model, err := elevenlabs.NewSpeechModel(ctx, elevenlabs.SpeechModelConfig{APIKey: "sentinel", BaseURL: endpoint, HTTPClient: client, DefaultOptions: speech.Options{Model: elevenlabs.ModelMultilingualV2, Voice: "voice"}})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &speech.Request{Text: "hello"})
			return err
		}},
		{name: "hume", header: "X-Hume-API-Key", call: func(ctx context.Context, endpoint string, client *http.Client) error {
			model, err := hume.NewSpeechModel(ctx, hume.SpeechModelConfig{APIKey: "sentinel", BaseURL: endpoint, HTTPClient: client, DefaultOptions: speech.Options{Model: hume.ModelOctave1, Voice: "voice"}})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &speech.Request{Text: "hello"})
			return err
		}},
		{name: "gladia", header: "x-gladia-key", call: func(ctx context.Context, endpoint string, client *http.Client) error {
			model, err := gladia.NewTranscriptionModel(ctx, gladia.TranscriptionModelConfig{APIKey: "sentinel", BaseURL: endpoint, HTTPClient: client, DefaultOptions: transcription.Options{Model: gladia.ModelSolaria1}})
			if err != nil {
				return err
			}
			audio, err := media.NewBytes("audio/wav", []byte("audio"))
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &transcription.Request{Audio: audio})
			return err
		}},
		{name: "jina", header: "Authorization", call: func(ctx context.Context, endpoint string, client *http.Client) error {
			model, err := jina.NewEmbeddingModel(ctx, jina.EmbeddingModelConfig{APIKey: "sentinel", BaseURL: endpoint, HTTPClient: client, DefaultOptions: embedding.Options{Model: "test"}})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &embedding.Request{Texts: []string{"hello"}})
			return err
		}},
		{name: "nomic", header: "Authorization", call: func(ctx context.Context, endpoint string, client *http.Client) error {
			model, err := nomic.NewEmbeddingModel(ctx, nomic.EmbeddingModelConfig{APIKey: "sentinel", BaseURL: endpoint, HTTPClient: client, DefaultOptions: embedding.Options{Model: "test"}})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &embedding.Request{Texts: []string{"hello"}})
			return err
		}},
		{name: "voyage", header: "Authorization", call: func(ctx context.Context, endpoint string, client *http.Client) error {
			model, err := voyage.NewEmbeddingModel(ctx, voyage.EmbeddingModelConfig{APIKey: "sentinel", BaseURL: endpoint, HTTPClient: client, DefaultOptions: embedding.Options{Model: "test"}})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &embedding.Request{Texts: []string{"hello"}})
			return err
		}},
		{name: "mistral", header: "Authorization", call: func(ctx context.Context, endpoint string, client *http.Client) error {
			model, err := mistral.NewChat(ctx, mistral.ChatConfig{APIKey: "sentinel", BaseURL: endpoint, HTTPClient: client, DefaultOptions: chat.Options{Model: "test"}})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}})
			return err
		}},
		{name: "cohere", header: "Authorization", call: func(ctx context.Context, endpoint string, client *http.Client) error {
			model, err := cohere.NewRerankModel(ctx, cohere.RerankModelConfig{APIKey: "sentinel", BaseURL: endpoint, HTTPClient: client, DefaultOptions: rerank.Options{Model: cohere.ModelRerankV35}})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &rerank.Request{Query: "query", Documents: []string{"document"}})
			return err
		}},
	}
}

type modelOriginTransport struct {
	location    string
	initial     int
	redirected  int
	first, last []originCredential
}

type originCredential struct{ name, value string }

func (m *modelOriginTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		defer request.Body.Close()
	}
	// The protocol's own wire tests select the native auth header. Here the
	// initial request is the reference: a redirect must preserve the same
	// single credential, without adding one under another name.
	var auth []originCredential
	for name, values := range request.Header {
		for _, v := range values {
			if v == "sentinel" || v == "Bearer sentinel" {
				auth = append(auth, originCredential{name, v})
			}
		}
	}
	status := http.StatusTemporaryRedirect
	header := http.Header{"Content-Type": {"application/json"}, "X-Should-Retry": {"false"}}
	var body io.ReadCloser = http.NoBody
	if request.URL.Path == "/finish" {
		m.redirected++
		status = http.StatusBadRequest
		m.last = auth
		body = io.NopCloser(strings.NewReader(`{"error":{"message":"test destination"}}`))
	} else {
		m.initial++
		m.first = auth
		header.Set("Location", m.location)
	}
	return &http.Response{StatusCode: status, Header: header, Body: body, Request: request}, nil
}

func TestModelAPIRequestsRemainWithinTheirCredentialOrigin(t *testing.T) {
	const endpoint = "https://api.scope.test"
	for _, provider := range modelOriginCalls() {
		for _, destination := range []struct {
			name, location string
			allowed        bool
		}{
			{name: "same origin", location: endpoint + "/finish", allowed: true},
			{name: "different host", location: "https://other.scope.test/finish"},
			{name: "different port", location: endpoint + ":9443/finish"},
			{name: "HTTPS downgrade", location: "http://api.scope.test/finish"},
		} {
			t.Run(provider.name+"/"+destination.name, func(t *testing.T) {
				transport := &modelOriginTransport{location: destination.location}
				client := &http.Client{Transport: transport}
				err := provider.call(t.Context(), endpoint, client)
				if transport.initial == 0 {
					t.Fatalf("call never reached API origin: %v", err)
				}
				wantCredentials := 1
				if provider.noAPIKey {
					wantCredentials = 0
				}
				if len(transport.first) != wantCredentials {
					t.Fatalf("original credential count: got %d, want %d", len(transport.first), wantCredentials)
				}
				if provider.header != "" && !strings.EqualFold(provider.header, transport.first[0].name) {
					t.Fatalf("credential header: got %s, want %s", transport.first[0].name, provider.header)
				}
				if destination.allowed {
					if errors.Is(err, httpclient.ErrOriginNotAllowed) || transport.redirected == 0 || !slices.Equal(transport.first, transport.last) {
						t.Fatalf("same-origin redirect changed credentials: before=%v after=%v redirected=%d error=%v", transport.first, transport.last, transport.redirected, err)
					}
				} else if transport.redirected != 0 || !errors.Is(err, httpclient.ErrOriginNotAllowed) {
					t.Fatalf("unauthorized destination: redirected=%d error=%v", transport.redirected, err)
				}
				if client.Transport != transport || client.CheckRedirect != nil {
					t.Fatal("caller-owned client was mutated")
				}
			})
		}
	}
}
