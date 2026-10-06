package google_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/genai"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/google"
	"github.com/Tangerg/scope/models/google/vertexai"
)

type nativeChat interface {
	chat.Model
	chat.Streamer
}

func newNativeChat(t *testing.T, provider string, server *httptest.Server) nativeChat {
	t.Helper()
	options := chat.Options{Model: "gemini-test"}
	if provider == "google" {
		model, err := google.NewChat(t.Context(), google.ChatConfig{APIKey: "test", BaseURL: server.URL, HTTPClient: server.Client(), DefaultOptions: options})
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

func newNativeTranscription(t *testing.T, provider string, server *httptest.Server) transcription.Model {
	t.Helper()
	options := transcription.Options{Model: "gemini-test"}
	if provider == "google" {
		model, err := google.NewTranscriptionModel(t.Context(), google.TranscriptionModelConfig{APIKey: "test", BaseURL: server.URL, HTTPClient: server.Client(), DefaultOptions: options})
		if err != nil {
			t.Fatal(err)
		}
		return model
	}
	model, err := vertexai.NewTranscriptionModel(t.Context(), vertexai.TranscriptionModelConfig{Client: vertexai.ClientConfig{Project: "project", Location: "global", BaseURL: server.URL, HTTPClient: server.Client()}, DefaultOptions: options})
	if err != nil {
		t.Fatal(err)
	}
	return model
}

func newNativeUnarySpeech(t *testing.T, provider string, server *httptest.Server) speech.Model {
	t.Helper()
	options := speech.Options{Model: google.ModelGemini25FlashPreviewTTS}
	if provider == "google" {
		model, err := google.NewSpeechModel(t.Context(), google.SpeechModelConfig{APIKey: "test", BaseURL: server.URL, HTTPClient: server.Client(), DefaultOptions: options})
		if err != nil {
			t.Fatal(err)
		}
		return model
	}
	model, err := vertexai.NewSpeechModel(t.Context(), vertexai.SpeechModelConfig{Client: vertexai.ClientConfig{Project: "project", Location: "global", BaseURL: server.URL, HTTPClient: server.Client()}, DefaultOptions: options})
	if err != nil {
		t.Fatal(err)
	}
	return model
}

func nativeTranscriptionRequest(t *testing.T) *transcription.Request {
	t.Helper()
	audio, err := media.NewBytes("audio/wav", []byte("input audio"))
	if err != nil {
		t.Fatal(err)
	}
	request, err := transcription.NewRequest(audio)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestNativeSDKErrorClassificationPreservesCause(t *testing.T) {
	for _, provider := range []string{"google", "vertexai"} {
		t.Run(provider, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"error":{"code":400,"message":"invalid request","status":"INVALID_ARGUMENT"}}`)
			}))
			defer server.Close()
			assertError := func(t *testing.T, err error) {
				t.Helper()
				var classified interface {
					HTTPStatus() int
					HTTPHeader() http.Header
				}
				if !errors.As(err, &classified) || classified.HTTPStatus() != http.StatusBadRequest {
					t.Fatalf("HTTP classification = %v", err)
				}
				cause, ok := errors.AsType[genai.APIError](err)
				if !ok || cause.Code != 400 || cause.Status != "INVALID_ARGUMENT" || cause.Message != "invalid request" {
					t.Fatalf("SDK cause = %#v, found %v", cause, ok)
				}
			}
			t.Run("stream", func(t *testing.T) {
				request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("hello")))
				if err != nil {
					t.Fatal(err)
				}
				response, err := newNativeChat(t, provider, server).Call(t.Context(), request)
				if response != nil {
					t.Fatalf("error returned response: %#v", response)
				}
				assertError(t, err)
			})
			t.Run("unary", func(t *testing.T) {
				response, err := newNativeTranscription(t, provider, server).Call(t.Context(), nativeTranscriptionRequest(t))
				if response != nil {
					t.Fatalf("error returned response: %#v", response)
				}
				assertError(t, err)
			})
		})
	}
}

func TestNativeCitationAdmission(t *testing.T) {
	for _, provider := range []string{"google", "vertexai"} {
		for _, sample := range []struct {
			name    string
			uri     string
			title   string
			invalid bool
		}{
			{name: "valid", uri: "https://example.com/source?q=%E4%B8%AD", title: "Source 世界"},
			{name: "relative URI", uri: "relative/source", title: "Source", invalid: true},
			{name: "padded URI", uri: " https://example.com/source", title: "Source", invalid: true},
			{name: "padded title", uri: "https://example.com/source", title: " Source ", invalid: true},
			{name: "no URI", title: "Source without a portable identity"},
		} {
			for _, text := range []string{"answer", ""} {
				t.Run(provider+"/"+sample.name+"/text="+text, func(t *testing.T) {
					citations, err := jsonv2.Marshal([]genai.Citation{{URI: sample.uri, Title: sample.title}})
					if err != nil {
						t.Fatal(err)
					}
					citationField := "citations"
					if provider == "google" {
						citationField = "citationSources"
					}
					contentField := ""
					if text != "" {
						contentField = fmt.Sprintf(`"content":{"parts":[{"text":%q}]},`, text)
					}
					data := fmt.Sprintf(`{"candidates":[{%s"finishReason":"STOP","citationMetadata":{%q:%s}}]}`, contentField, citationField, citations)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"prefix \"}]}}]}\n\n")
						fmt.Fprintf(w, "data: %s\n\n", data)
					}))
					defer server.Close()
					model := newNativeChat(t, provider, server)
					request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("hello")))
					if err != nil {
						t.Fatal(err)
					}
					assertResponse := func(t *testing.T, response *chat.Response) {
						t.Helper()
						if response.Text() != "prefix "+text {
							t.Fatalf("response text = %q", response.Text())
						}
						citations := response.Output.Message.Parts[0].Citations
						if sample.uri == "" {
							if len(citations) != 0 {
								t.Fatalf("source-less citations = %#v", citations)
							}
							return
						}
						if len(citations) != 1 || citations[0].Source.Value != sample.uri || citations[0].Title != sample.title {
							t.Fatalf("citations = %#v", citations)
						}
					}
					t.Run("call", func(t *testing.T) {
						response, callErr := model.Call(t.Context(), request)
						if sample.invalid {
							if response != nil || !errors.Is(callErr, chat.ErrInvalidCitation) {
								t.Fatalf("invalid citation: response = %#v, error = %v", response, callErr)
							}
							return
						}
						if callErr != nil {
							t.Fatal(callErr)
						}
						assertResponse(t, response)
					})
					t.Run("stream", func(t *testing.T) {
						var accumulator chat.ResponseAccumulator
						var streamErr error
						errorsObserved := 0
						for delta, deltaErr := range model.Stream(t.Context(), request) {
							if deltaErr != nil {
								if delta != nil {
									t.Fatal("failed citation mapping yielded a response")
								}
								streamErr = deltaErr
								errorsObserved++
								continue
							}
							if addErr := accumulator.Add(delta); addErr != nil {
								t.Fatal(addErr)
							}
						}
						if sample.invalid {
							if errorsObserved != 1 || !errors.Is(streamErr, chat.ErrInvalidCitation) {
								t.Fatalf("invalid citation: errors = %d, error = %v", errorsObserved, streamErr)
							}
							if response, completeErr := accumulator.Response(); completeErr == nil || response != nil {
								t.Fatalf("failed stream completed: response = %#v, error = %v", response, completeErr)
							}
							return
						}
						if streamErr != nil {
							t.Fatal(streamErr)
						}
						response, completeErr := accumulator.Response()
						if completeErr != nil {
							t.Fatal(completeErr)
						}
						assertResponse(t, response)
					})
				})
			}
		}
	}
}

func TestNativeThoughtSignaturesSurviveStreamHistoryAndReplay(t *testing.T) {
	want := []*genai.Part{
		{Text: "answer"},
		{ThoughtSignature: []byte("empty-text-signature")},
		{Thought: true, Text: "first", ThoughtSignature: []byte("signature-A")},
		{Thought: true, Text: "second", ThoughtSignature: []byte("signature-B")},
		{Text: "signed", ThoughtSignature: []byte("same-signature")},
		{Text: "signed", ThoughtSignature: []byte("same-signature")},
		{Text: "unsigned"},
	}
	for _, provider := range []string{"google", "vertexai"} {
		t.Run(provider, func(t *testing.T) {
			var requests atomic.Int64
			replayed := make(chan []*genai.Part, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if requests.Add(1) == 1 {
					for _, part := range want {
						data, err := jsonv2.Marshal(&genai.GenerateContentResponse{Candidates: []*genai.Candidate{{Content: &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{part}}}}})
						if err != nil {
							t.Error(err)
							return
						}
						fmt.Fprintf(w, "data: %s\n\n", data)
					}
				} else {
					var request struct {
						Contents []*genai.Content `json:"contents"`
					}
					if err := jsonv2.UnmarshalRead(r.Body, &request); err != nil {
						t.Error(err)
						return
					}
					if len(request.Contents) != 3 {
						t.Errorf("contents = %#v", request.Contents)
						return
					}
					replayed <- request.Contents[1].Parts
					fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"continued\"}]}}]}\n\n")
				}
				fmt.Fprint(w, "data: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n")
			}))
			defer server.Close()
			model := newNativeChat(t, provider, server)
			user := chat.NewUserMessage(chat.NewTextPart("hello"))
			request, err := chat.NewRequest(user)
			if err != nil {
				t.Fatal(err)
			}
			var accumulator chat.ResponseAccumulator
			for delta, err := range model.Stream(t.Context(), request) {
				if err != nil {
					t.Fatal(err)
				}
				if err := accumulator.Add(delta); err != nil {
					t.Fatal(err)
				}
			}
			response, err := accumulator.Response()
			if err != nil {
				t.Fatal(err)
			}
			data, err := jsonv2.Marshal(response.Output.Message)
			if err != nil {
				t.Fatal(err)
			}
			var history chat.Message
			if decodeErr := jsonv2.Unmarshal(data, &history); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if len(history.Parts) != len(want) || history.Parts[1].Kind != chat.PartReasoning || history.Parts[1].Text != "" || string(history.Parts[1].ReasoningState) != "empty-text-signature" {
				t.Fatalf("persisted parts = %#v", history.Parts)
			}
			request, err = chat.NewRequest(user, history, chat.NewUserMessage(chat.NewTextPart("continue")))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := model.Call(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-replayed:
				if !reflect.DeepEqual(got, want) {
					gotJSON, _ := jsonv2.Marshal(got)
					wantJSON, _ := jsonv2.Marshal(want)
					t.Fatalf("replay = %s, want %s", gotJSON, wantJSON)
				}
			default:
				t.Fatal("next request did not replay the assistant history")
			}
		})
	}
}

func TestNativeUnaryMediaRequiresSuccessfulCompletion(t *testing.T) {
	for _, provider := range []string{"google", "vertexai"} {
		for _, finish := range []string{"STOP", "MAX_TOKENS", "SAFETY", "FINISH_REASON_UNSPECIFIED", ""} {
			t.Run(provider+"/"+finish, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprintf(w, `{"candidates":[{"content":{"parts":[{"text":"transcript"},{"inlineData":{"mimeType":"audio/L16;rate=24000","data":"YWJj"}}]},"finishReason":%q}]}`, finish)
				}))
				defer server.Close()
				t.Run("transcription", func(t *testing.T) {
					response, err := newNativeTranscription(t, provider, server).Call(t.Context(), nativeTranscriptionRequest(t))
					if finish == "STOP" {
						if err != nil || response == nil || response.Output.Text != "transcript" {
							t.Fatalf("completed transcription = %#v, %v", response, err)
						}
						return
					}
					if response != nil || !errors.Is(err, transcription.ErrInvalidResponse) || !strings.Contains(err.Error(), finish) {
						t.Fatalf("incomplete transcription = %#v, %v", response, err)
					}
				})
				t.Run("speech", func(t *testing.T) {
					request, err := speech.NewRequest("hello")
					if err != nil {
						t.Fatal(err)
					}
					response, err := newNativeUnarySpeech(t, provider, server).Call(t.Context(), request)
					if finish == "STOP" {
						if err != nil || response == nil || string(response.Output.Audio) != "abc" {
							t.Fatalf("completed speech = %#v, %v", response, err)
						}
						return
					}
					if response != nil || !errors.Is(err, speech.ErrInvalidResponse) || !strings.Contains(err.Error(), finish) {
						t.Fatalf("incomplete speech = %#v, %v", response, err)
					}
				})
			})
		}
	}
}
