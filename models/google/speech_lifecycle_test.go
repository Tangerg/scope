package google_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	tts "github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/google"
	"github.com/Tangerg/scope/models/google/vertexai"
)

type speechSynthesis interface {
	tts.Model
	tts.Streamer
}

func newStreamingSpeech(t *testing.T, provider string, server *httptest.Server) speechSynthesis {
	t.Helper()
	options := tts.Options{Model: google.ModelGemini31FlashTTSPreview, Voice: "Kore"}
	if provider == "google" {
		model, err := google.NewStreamingAudioTTSModel(t.Context(), google.AudioTTSModelConfig{APIKey: "test", BaseURL: server.URL, HTTPClient: server.Client(), DefaultOptions: options})
		if err != nil {
			t.Fatal(err)
		}
		return model
	}
	model, err := vertexai.NewStreamingAudioTTSModel(t.Context(), vertexai.AudioTTSModelConfig{Client: vertexai.ClientConfig{Project: "project", Location: "global", BaseURL: server.URL, HTTPClient: server.Client()}, DefaultOptions: options})
	if err != nil {
		t.Fatal(err)
	}
	return model
}

func writeSpeechAudio(w http.ResponseWriter, audio string) {
	fmt.Fprintf(w, "data: {\"modelVersion\":\"served-model\",\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"inlineData\":{\"mimeType\":\"audio/L16;rate=24000\",\"data\":%q}}]}}]}\n\n", base64.StdEncoding.EncodeToString([]byte(audio)))
}

func TestStreamingSpeechAggregateAndStream(t *testing.T) {
	for _, provider := range []string{"google", "vertexai"} {
		t.Run(provider, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.Contains(r.URL.Path, google.ModelGemini31FlashTTSPreview+":streamGenerateContent") {
					t.Errorf("wrong endpoint: %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				writeSpeechAudio(w, "first")
				writeSpeechAudio(w, "second")
				fmt.Fprint(w, "data: {\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"totalTokenCount\":12}}\n\n")
			}))
			defer server.Close()
			model := newStreamingSpeech(t, provider, server)
			req, _ := tts.NewRequest("hello")
			aggregate, err := model.Call(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			var audio []byte
			var last *tts.Response
			chunks := 0
			for chunk, err := range model.Stream(t.Context(), req) {
				if err != nil {
					t.Fatal(err)
				}
				audio = append(audio, chunk.Output.Audio...)
				last = chunk
				chunks++
			}
			if chunks != 2 || string(audio) != "firstsecond" || string(aggregate.Output.Audio) != string(audio) {
				t.Fatalf("chunks=%d aggregate=%q stream=%q", chunks, aggregate.Output.Audio, audio)
			}
			if !reflect.DeepEqual(aggregate.Metadata, last.Metadata) || aggregate.Metadata.Model != "served-model" {
				t.Fatalf("metadata mismatch: %+v %+v", aggregate.Metadata, last.Metadata)
			}
			native, found, err := aggregate.Metadata.Extra.Decode[map[string]any](provider + "/speech_response")
			if err != nil || !found || native["usageMetadata"] == nil {
				t.Fatalf("terminal metadata lost: %v, %v", native, err)
			}
		})
	}
}

func TestStreamingSpeechRejectsIncompleteResults(t *testing.T) {
	for _, provider := range []string{"google", "vertexai"} {
		for _, ending := range []string{"truncated", "provider error", "blocked", "empty"} {
			t.Run(provider+"/"+ending, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					if ending != "empty" {
						writeSpeechAudio(w, "first")
						writeSpeechAudio(w, "second")
					}
					switch ending {
					case "provider error":
						fmt.Fprint(w, "data: {\"error\":{\"code\":500,\"message\":\"failed\",\"status\":\"INTERNAL\"}}\n\n")
					case "blocked":
						fmt.Fprint(w, "data: {\"candidates\":[{\"finishReason\":\"SAFETY\"}]}\n\n")
					case "empty":
						fmt.Fprint(w, "data: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n")
					}
				}))
				defer server.Close()
				model := newStreamingSpeech(t, provider, server)
				req, _ := tts.NewRequest("hello")
				response, err := model.Call(t.Context(), req)
				if ending == "truncated" && !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatalf("truncation error = %v", err)
				}
				if err == nil || response != nil {
					t.Fatalf("partial aggregate accepted: %v, %v", response, err)
				}
				var streamErr error
				for _, err := range model.Stream(t.Context(), req) {
					if err != nil {
						streamErr = err
					}
				}
				if streamErr == nil {
					t.Fatal("stream accepted incomplete result")
				}
			})
		}
	}
}

func TestStreamingSpeechReleasesTransport(t *testing.T) {
	for _, provider := range []string{"google", "vertexai"} {
		for _, operation := range []string{"call cancel", "stream cancel", "early stop"} {
			t.Run(provider+"/"+operation, func(t *testing.T) {
				started := make(chan struct{})
				closed := make(chan struct{})
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					writeSpeechAudio(w, "first")
					writeSpeechAudio(w, "second")
					w.(http.Flusher).Flush()
					close(started)
					<-r.Context().Done()
					close(closed)
				}))
				defer server.Close()
				model := newStreamingSpeech(t, provider, server)
				req, _ := tts.NewRequest("hello")
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if operation == "call cancel" {
					done := make(chan error, 1)
					go func() {
						response, err := model.Call(ctx, req)
						if response != nil {
							err = fmt.Errorf("partial aggregate returned")
						}
						done <- err
					}()
					select {
					case <-started:
					case <-time.After(5 * time.Second):
						t.Fatal("request did not start")
					}
					cancel()
					select {
					case err := <-done:
						if !errors.Is(err, context.Canceled) {
							t.Fatalf("cancel error=%v", err)
						}
					case <-time.After(5 * time.Second):
						t.Fatal("call did not stop")
					}
				} else {
					var streamErr error
					for _, err := range model.Stream(ctx, req) {
						if err != nil {
							streamErr = err
							break
						}
						if operation == "early stop" {
							break
						}
						cancel()
					}
					if operation == "stream cancel" && !errors.Is(streamErr, context.Canceled) {
						t.Fatalf("cancel error=%v", streamErr)
					}
				}
				select {
				case <-closed:
				case <-time.After(5 * time.Second):
					t.Fatal("transport remained open")
				}
			})
		}
	}
}

func TestSpeechModelsHaveDisjointCapabilities(t *testing.T) {
	for _, provider := range []string{"google", "vertexai"} {
		t.Run(provider, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(http.StatusBadRequest) }))
			defer server.Close()
			newUnary := func(model string) (tts.Model, error) {
				options := tts.Options{Model: model}
				if provider == "google" {
					return google.NewAudioTTSModel(t.Context(), google.AudioTTSModelConfig{APIKey: "test", BaseURL: server.URL, HTTPClient: server.Client(), DefaultOptions: options})
				}
				return vertexai.NewAudioTTSModel(t.Context(), vertexai.AudioTTSModelConfig{Client: vertexai.ClientConfig{Project: "project", Location: "global", BaseURL: server.URL, HTTPClient: server.Client()}, DefaultOptions: options})
			}
			if _, err := newUnary(google.ModelGemini31FlashTTSPreview); err == nil {
				t.Fatal("3.1 retained an independent unary execution path")
			}
			for _, name := range []string{google.ModelGemini25FlashPreviewTTS, google.ModelGemini25ProPreviewTTS} {
				model, err := newUnary(name)
				if err != nil {
					t.Fatal(err)
				}
				req, _ := tts.NewRequest("hello")
				req.Options.Model = google.ModelGemini31FlashTTSPreview
				if response, err := model.Call(t.Context(), req); err == nil || response != nil {
					t.Fatal("request override crossed unary capability")
				}
			}
			streaming := newStreamingSpeech(t, provider, server)
			req, _ := tts.NewRequest("hello")
			req.Options.Model = google.ModelGemini25FlashPreviewTTS
			if response, err := streaming.Call(t.Context(), req); err == nil || response != nil {
				t.Fatal("request override crossed streaming capability")
			}
			if requests != 0 {
				t.Fatalf("invalid capability reached transport %d times", requests)
			}
		})
	}
}
