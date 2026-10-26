package providerconformance_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/assemblyai"
	"github.com/Tangerg/scope/models/gladia"
	"github.com/Tangerg/scope/models/revai"
)

type asyncTranscriptionProvider struct {
	name, extensionKey, pending, completed string
	build                                  func(context.Context, string) (transcription.Model, error)
}

func asyncTranscriptionProviders() []asyncTranscriptionProvider {
	return []asyncTranscriptionProvider{
		{"assemblyai", assemblyai.RequestExtensionKey, `{"id":"job-1","status":"queued"}`, `{"id":"job-1","status":"completed","text":"transcript"}`, func(ctx context.Context, base string) (transcription.Model, error) {
			return assemblyai.NewTranscriptionModel(ctx, assemblyai.TranscriptionModelConfig{APIKey: "test", BaseURL: base, DefaultOptions: transcription.Options{Model: assemblyai.ModelUniversal2}})
		}},
		{"gladia", gladia.RequestExtensionKey, `{"id":"job-1"}`, `{"id":"job-1","status":"done","result":{"transcription":{"full_transcript":"transcript"}}}`, func(ctx context.Context, base string) (transcription.Model, error) {
			return gladia.NewTranscriptionModel(ctx, gladia.TranscriptionModelConfig{APIKey: "test", BaseURL: base, DefaultOptions: transcription.Options{Model: gladia.ModelSolaria1}})
		}},
		{"revai", revai.RequestExtensionKey, `{"id":"job-1","status":"in_progress"}`, `{"id":"job-1","status":"transcribed"}`, func(ctx context.Context, base string) (transcription.Model, error) {
			return revai.NewTranscriptionModel(ctx, revai.TranscriptionModelConfig{APIKey: "test", BaseURL: base, DefaultOptions: transcription.Options{Model: revai.ModelMachine}})
		}},
	}
}

func TestAsyncTranscriptionRejectsUnmappedOptionsBeforeIO(t *testing.T) {
	for _, provider := range asyncTranscriptionProviders() {
		for _, extension := range []string{`{"diarization_enabled":true}`, `null`} {
			t.Run(provider.name+"/"+extension, func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
					calls.Add(1)
					writer.WriteHeader(http.StatusBadRequest)
				}))
				t.Cleanup(server.Close)
				model, err := provider.build(t.Context(), server.URL)
				if err != nil {
					t.Fatal(err)
				}
				audio, err := media.NewURI("audio/mpeg", "https://audio.test/input.mp3")
				if err != nil {
					t.Fatal(err)
				}
				request := &transcription.Request{Audio: audio}
				if setErr := request.Options.Extensions.Set(provider.extensionKey, json.RawMessage(extension)); setErr != nil {
					t.Fatal(setErr)
				}
				response, err := model.Call(t.Context(), request)
				if err == nil || response != nil || calls.Load() != 0 {
					t.Fatalf("invalid extension: response=%v error=%v provider_calls=%d", response, err, calls.Load())
				}
			})
		}
	}
}

func TestAsyncTranscriptionRejectsMissingOrChangedJobIdentity(t *testing.T) {
	for _, provider := range asyncTranscriptionProviders() {
		for _, test := range []struct {
			name, pending, completed string
		}{
			{"missing_creation_id", strings.Replace(provider.pending, `"id":"job-1"`, `"id":""`, 1), provider.completed},
			{"missing_poll_id", provider.pending, strings.Replace(provider.completed, `"id":"job-1"`, `"id":""`, 1)},
			{"changed_poll_id", provider.pending, strings.Replace(provider.completed, `"id":"job-1"`, `"id":"job-2"`, 1)},
		} {
			t.Run(provider.name+"/"+test.name, func(t *testing.T) {
				var transcriptFetches atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					writer.Header().Set("Content-Type", "application/json")
					switch {
					case request.Method == http.MethodPost:
						fmt.Fprint(writer, test.pending)
					case strings.HasSuffix(request.URL.Path, "/transcript"):
						transcriptFetches.Add(1)
						writer.Header().Set("Content-Type", "text/plain")
						fmt.Fprint(writer, "transcript")
					default:
						fmt.Fprint(writer, test.completed)
					}
				}))
				t.Cleanup(server.Close)
				model, err := provider.build(t.Context(), server.URL)
				if err != nil {
					t.Fatal(err)
				}
				audio, err := media.NewURI("audio/mpeg", "https://audio.test/input.mp3")
				if err != nil {
					t.Fatal(err)
				}
				response, err := model.Call(t.Context(), &transcription.Request{Audio: audio})
				if !errors.Is(err, transcription.ErrInvalidResponse) || response != nil || transcriptFetches.Load() != 0 {
					t.Fatalf("unbound job response: response=%v error=%v transcript_fetches=%d", response, err, transcriptFetches.Load())
				}
			})
		}
	}
}
