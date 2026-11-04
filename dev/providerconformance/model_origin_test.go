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
	"github.com/Tangerg/scope/models/assemblyai"
	"github.com/Tangerg/scope/models/bedrock"
	"github.com/Tangerg/scope/models/blackforestlabs"
	"github.com/Tangerg/scope/models/cohere"
	"github.com/Tangerg/scope/models/deepgram"
	"github.com/Tangerg/scope/models/elevenlabs"
	"github.com/Tangerg/scope/models/gladia"
	"github.com/Tangerg/scope/models/google"
	"github.com/Tangerg/scope/models/google/vertexai"
	"github.com/Tangerg/scope/models/hume"
	"github.com/Tangerg/scope/models/jina"
	"github.com/Tangerg/scope/models/mistral"
	"github.com/Tangerg/scope/models/nomic"
	"github.com/Tangerg/scope/models/ollama"
	"github.com/Tangerg/scope/models/openai"
	"github.com/Tangerg/scope/models/openrouter"
	"github.com/Tangerg/scope/models/prodia"
	"github.com/Tangerg/scope/models/replicate"
	"github.com/Tangerg/scope/models/revai"
	"github.com/Tangerg/scope/models/stability"
	"github.com/Tangerg/scope/models/voyage"
	"github.com/Tangerg/scope/models/xiaomi"
)

type modelOriginCall struct {
	name            string
	header          string // when fixed by this binding rather than its external SDK
	noAPIKey        bool
	safeHTTPError   bool
	rejectRedirects bool
	call            func(context.Context, string, *http.Client) error
}

func modelOriginCalls() []modelOriginCall {
	return []modelOriginCall{
		{name: "bedrock", header: "X-Amz-Security-Token", rejectRedirects: true, call: func(ctx context.Context, endpoint string, client *http.Client) error {
			model, err := bedrock.NewEmbeddingModel(ctx, bedrock.EmbeddingModelConfig{
				Region: "us-east-1", BaseURL: endpoint, HTTPClient: client,
				Credentials:    &bedrock.Credentials{AccessKeyID: "test-key", SecretAccessKey: "test-secret", SessionToken: "sentinel"},
				DefaultOptions: embedding.Options{Model: "amazon.titan-embed-text-v2:0"},
			})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &embedding.Request{Texts: []string{"private document"}})
			return err
		}},
		{name: "assemblyai", safeHTTPError: true, header: "Authorization", call: func(ctx context.Context, endpoint string, client *http.Client) error {
			model, err := assemblyai.NewTranscriptionModel(ctx, assemblyai.TranscriptionModelConfig{APIKey: "sentinel", BaseURL: endpoint, HTTPClient: client, DefaultOptions: transcription.Options{Model: assemblyai.ModelUniversal2}})
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
		{name: "deepgram", safeHTTPError: true, header: "Authorization", call: func(ctx context.Context, endpoint string, client *http.Client) error {
			model, err := deepgram.NewTranscriptionModel(ctx, deepgram.TranscriptionModelConfig{APIKey: "sentinel", BaseURL: endpoint, HTTPClient: client, DefaultOptions: transcription.Options{Model: "nova-3"}})
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
		{name: "revai", safeHTTPError: true, header: "Authorization", call: func(ctx context.Context, endpoint string, client *http.Client) error {
			model, err := revai.NewTranscriptionModel(ctx, revai.TranscriptionModelConfig{APIKey: "sentinel", BaseURL: endpoint, HTTPClient: client, DefaultOptions: transcription.Options{Model: revai.ModelMachine}})
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
		{name: "prodia", safeHTTPError: true, header: "Authorization", call: func(ctx context.Context, endpoint string, client *http.Client) error {
			model, err := prodia.NewImageModel(ctx, prodia.ImageModelConfig{APIKey: "sentinel", BaseURL: endpoint, HTTPClient: client, DefaultOptions: image.Options{Model: prodia.JobFlux2DevTextToImage}})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &image.Request{Prompt: "image"})
			return err
		}},
		{name: "stability", safeHTTPError: true, header: "Authorization", call: func(ctx context.Context, endpoint string, client *http.Client) error {
			model, err := stability.NewImageModel(ctx, stability.ImageModelConfig{APIKey: "sentinel", BaseURL: endpoint, HTTPClient: client, DefaultOptions: image.Options{Model: stability.ModelCore}})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &image.Request{Prompt: "image"})
			return err
		}},
		{name: "replicate", safeHTTPError: true, header: "Authorization", call: func(ctx context.Context, endpoint string, client *http.Client) error {
			model, err := replicate.NewImageModel(ctx, replicate.ImageModelConfig{APIKey: "sentinel", BaseURL: endpoint, HTTPClient: client, DefaultOptions: image.Options{Model: replicate.ModelFluxSchnell}, InputSchema: replicate.FluxSchnellImageInputSchema()})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &image.Request{Prompt: "image"})
			return err
		}},
		{name: "ollama chat", noAPIKey: true, call: func(ctx context.Context, endpoint string, client *http.Client) error {
			model, err := ollama.NewChat(ctx, ollama.ChatConfig{BaseURL: endpoint, HTTPClient: client, DefaultOptions: chat.Options{Model: "test"}})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("private prompt"))}})
			return err
		}},
		{name: "ollama embedding", noAPIKey: true, call: func(ctx context.Context, endpoint string, client *http.Client) error {
			model, err := ollama.NewEmbeddingModel(ctx, ollama.EmbeddingModelConfig{BaseURL: endpoint, HTTPClient: client, DefaultOptions: embedding.Options{Model: "test"}})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &embedding.Request{Texts: []string{"private document"}})
			return err
		}},
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
		{name: "google interactions", safeHTTPError: true, header: "x-goog-api-key", call: func(ctx context.Context, endpoint string, client *http.Client) error {
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
		{name: "blackforestlabs", safeHTTPError: true, header: "x-key", call: func(ctx context.Context, endpoint string, client *http.Client) error {
			model, err := blackforestlabs.NewImageModel(ctx, blackforestlabs.ImageModelConfig{APIKey: "sentinel", BaseURL: endpoint, HTTPClient: client, DefaultOptions: image.Options{Model: blackforestlabs.ModelFlux11Pro}})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &image.Request{Prompt: "image"})
			return err
		}},
		{name: "elevenlabs", safeHTTPError: true, header: "xi-api-key", call: func(ctx context.Context, endpoint string, client *http.Client) error {
			model, err := elevenlabs.NewSpeechModel(ctx, elevenlabs.SpeechModelConfig{APIKey: "sentinel", BaseURL: endpoint, HTTPClient: client, DefaultOptions: speech.Options{Model: elevenlabs.ModelMultilingualV2, Voice: "voice"}})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &speech.Request{Text: "hello"})
			return err
		}},
		{name: "hume", safeHTTPError: true, header: "X-Hume-API-Key", call: func(ctx context.Context, endpoint string, client *http.Client) error {
			model, err := hume.NewSpeechModel(ctx, hume.SpeechModelConfig{APIKey: "sentinel", BaseURL: endpoint, HTTPClient: client, DefaultOptions: speech.Options{Model: hume.ModelOctave1, Voice: "voice"}})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &speech.Request{Text: "hello"})
			return err
		}},
		{name: "gladia", safeHTTPError: true, header: "x-gladia-key", call: func(ctx context.Context, endpoint string, client *http.Client) error {
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
		{name: "jina", safeHTTPError: true, header: "Authorization", call: func(ctx context.Context, endpoint string, client *http.Client) error {
			model, err := jina.NewEmbeddingModel(ctx, jina.EmbeddingModelConfig{APIKey: "sentinel", BaseURL: endpoint, HTTPClient: client, DefaultOptions: embedding.Options{Model: "test"}})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &embedding.Request{Texts: []string{"hello"}})
			return err
		}},
		{name: "nomic", safeHTTPError: true, header: "Authorization", call: func(ctx context.Context, endpoint string, client *http.Client) error {
			model, err := nomic.NewEmbeddingModel(ctx, nomic.EmbeddingModelConfig{APIKey: "sentinel", BaseURL: endpoint, HTTPClient: client, DefaultOptions: embedding.Options{Model: "test"}})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &embedding.Request{Texts: []string{"hello"}})
			return err
		}},
		{name: "voyage", safeHTTPError: true, header: "Authorization", call: func(ctx context.Context, endpoint string, client *http.Client) error {
			model, err := voyage.NewEmbeddingModel(ctx, voyage.EmbeddingModelConfig{APIKey: "sentinel", BaseURL: endpoint, HTTPClient: client, DefaultOptions: embedding.Options{Model: "test"}})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &embedding.Request{Texts: []string{"hello"}})
			return err
		}},
		{name: "mistral", safeHTTPError: true, header: "Authorization", call: func(ctx context.Context, endpoint string, client *http.Client) error {
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
	status      int
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
			if v == "sentinel" || v == "Bearer sentinel" || v == "Token sentinel" {
				auth = append(auth, originCredential{name, v})
			}
		}
	}
	status := http.StatusTemporaryRedirect
	if m.status != 0 {
		status = m.status
	}
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
				if provider.rejectRedirects {
					transport.status = http.StatusFound
				}
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
				if destination.allowed && provider.rejectRedirects {
					if err == nil || errors.Is(err, httpclient.ErrOriginNotAllowed) || transport.redirected != 0 {
						t.Fatalf("signed request followed a redirect: redirected=%d error=%v", transport.redirected, err)
					}
				} else if destination.allowed {
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
