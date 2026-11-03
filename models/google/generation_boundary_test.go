package google_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"google.golang.org/genai"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/google"
	"github.com/Tangerg/scope/models/google/vertexai"
)

func TestStreamingSpeechPreflightEvidence(t *testing.T) {
	for _, provider := range []string{"google", "vertexai"} {
		t.Run(provider, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusBadRequest)
			}))
			t.Cleanup(server.Close)
			model := newStreamingSpeech(t, provider, server)
			for name, request := range map[string]*speech.Request{"nil": nil, "empty": {}} {
				t.Run(name, func(t *testing.T) {
					response, err := model.Call(t.Context(), request)
					if response != nil || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted || !errors.Is(err, speech.ErrInvalidRequest) {
						t.Fatalf("Call response=%v error=%v", response, err)
					}
					failures := 0
					for chunk, streamErr := range model.Stream(t.Context(), request) {
						if chunk != nil || invocation.ClassifyFailure(streamErr) != invocation.FailureNotExecuted || !errors.Is(streamErr, speech.ErrInvalidRequest) {
							t.Fatalf("Stream chunk=%v error=%v", chunk, streamErr)
						}
						failures++
					}
					if failures != 1 || calls.Load() != 0 {
						t.Fatalf("failures=%d provider calls=%d", failures, calls.Load())
					}
				})
			}
		})
	}
}

func TestStreamingSpeechOwnsOneNativeGeneration(t *testing.T) {
	const audio = `{"content":{"parts":[{"inlineData":{"mimeType":"audio/L16;rate=24000","data":"YQ=="}}]}}`
	const stop = `{"candidates":[{"index":0,"finishReason":"STOP"}]}`
	cases := map[string][]string{
		"wrong candidate index":           {`{"candidates":[{"index":1,"content":{"parts":[{"inlineData":{"mimeType":"audio/L16;rate=24000","data":"YQ=="}}]}}]}`, stop},
		"multiple candidates":             {`{"candidates":[` + audio + `,` + audio + `]}`, stop},
		"changed response identity":       {`{"responseId":"A","candidates":[` + audio + `]}`, `{"responseId":"B","candidates":[` + audio + `]}`, stop},
		"changed model identity":          {`{"modelVersion":"A","candidates":[` + audio + `]}`, `{"modelVersion":"B","candidates":[` + audio + `]}`, stop},
		"duplicate terminal":              {`{"candidates":[` + audio + `]}`, stop, stop},
		"blocked metadata after terminal": {`{"candidates":[` + audio + `]}`, stop, `{"promptFeedback":{"blockReason":"SAFETY"}}`},
		"audio after terminal":            {`{"candidates":[` + audio + `]}`, stop, `{"candidates":[` + audio + `]}`},
	}
	for _, provider := range []string{"google", "vertexai"} {
		for name, events := range cases {
			t.Run(provider+"/"+name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					for _, event := range events {
						fmt.Fprintf(w, "data: %s\n\n", event)
					}
				}))
				t.Cleanup(server.Close)
				model := newStreamingSpeech(t, provider, server)
				request := &speech.Request{Text: "hello"}
				if response, err := model.Call(t.Context(), request); err == nil || response != nil {
					t.Fatalf("invalid aggregate accepted: response=%v error=%v", response, err)
				}
				failures := 0
				for _, err := range model.Stream(t.Context(), request) {
					if err != nil {
						failures++
					}
				}
				if failures != 1 {
					t.Fatalf("stream failures=%d, want 1", failures)
				}
			})
		}
	}
}

func TestGenerationMetadataCannotSupplyCredentials(t *testing.T) {
	for _, provider := range []string{"google", "vertexai"} {
		for _, header := range []string{"x-goog-api-key", "X-Goog-Api-Key", "X-gOoG-aPi-KeY", "Authorization", "Cookie", "Proxy-Authorization"} {
			t.Run(provider+"/"+header, func(t *testing.T) {
				var calls atomic.Int64
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					calls.Add(1)
					w.WriteHeader(http.StatusBadRequest)
				}))
				t.Cleanup(server.Close)
				model := newGenerationChat(t, provider, server)
				request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}}
				if err := request.Options.Extensions.Set(provider+"/request", map[string]any{"httpOptions": map[string]any{"headers": http.Header{header: {"competing-key"}}}}); err != nil {
					t.Fatal(err)
				}
				response, err := model.Call(t.Context(), request)
				if response != nil || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted || calls.Load() != 0 {
					t.Fatalf("response=%v error=%v calls=%d", response, err, calls.Load())
				}
			})
		}
	}
}

func TestContinuationIsIncompleteOutput(t *testing.T) {
	for _, provider := range []string{"google", "vertexai"} {
		t.Run(provider, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"responseId\":\"A\",\"modelVersion\":\"test-model\",\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"partial\"}]},\"finishReason\":\"CONTINUATION\"}],\"continuationToken\":\"resume-token\"}\n\n")
			}))
			t.Cleanup(server.Close)
			model := newGenerationChat(t, provider, server)
			request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("continue"))}}
			response, err := model.Call(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if response.Text() != "partial" || response.Output.FinishReason != chat.FinishReasonLength {
				t.Fatalf("response=%v", response)
			}
			native, found, err := response.Metadata.Extra.Decode[genai.GenerateContentResponse](provider + "/response")
			if err != nil || !found || native.SDKHTTPResponse == nil {
				t.Fatalf("continuation metadata=%v found=%v error=%v", native, found, err)
			}
			var original struct {
				ContinuationToken string `json:"continuationToken"`
			}
			if err := jsonv2.Unmarshal([]byte(native.SDKHTTPResponse.Body), &original); err != nil || original.ContinuationToken != "resume-token" {
				t.Fatalf("preserved continuation token=%q error=%v", original.ContinuationToken, err)
			}
		})
	}
}

func newGenerationChat(t *testing.T, provider string, server *httptest.Server) chat.Model {
	t.Helper()
	options := chat.Options{Model: "test-model"}
	if provider == "google" {
		model, err := google.NewChat(t.Context(), google.ChatConfig{APIKey: "bound-key", BaseURL: server.URL, HTTPClient: server.Client(), DefaultOptions: options})
		if err != nil {
			t.Fatal(err)
		}
		return model
	}
	model, err := vertexai.NewChat(t.Context(), vertexai.ChatConfig{Client: vertexai.ClientConfig{Project: "project", Location: "global", BaseURL: server.URL, HTTPClient: server.Client()}, DefaultOptions: options})
	if err != nil {
		t.Fatal(err)
	}
	return model
}
