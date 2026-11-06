package deepgram_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/deepgram"
)

func TestSpeechRejectsCallbackDeliveryBeforeIO(t *testing.T) {
	for _, field := range []string{"callback", "callback_method"} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", field, streaming), func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					calls++
					if request.URL.Query().Has("callback") {
						writer.Header().Set("Content-Type", "application/json")
						fmt.Fprint(writer, `{"request_id":"async-speech"}`)
						return
					}
					writer.Header().Set("Content-Type", "audio/mpeg")
					fmt.Fprint(writer, "audio")
				}))
				t.Cleanup(server.Close)
				options := speech.Options{Model: "aura-2-thalia-en"}
				value := "https://callback.test/result"
				if field == "callback_method" {
					value = http.MethodPut
				}
				if err := options.Extensions.Set(deepgram.SpeechRequestExtensionKey, map[string]any{"extra": url.Values{field: {value}}}); err != nil {
					t.Fatal(err)
				}
				model, err := deepgram.NewSpeechModel(t.Context(), deepgram.SpeechModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: options})
				if err != nil {
					t.Fatal(err)
				}
				request := &speech.Request{Text: "hello"}
				var response *speech.Response
				if streaming {
					for chunk, streamErr := range model.Stream(t.Context(), request) {
						response, err = chunk, streamErr
						if response != nil {
							t.Errorf("callback delivery options reached the audio stream: %q", response.Output.Audio)
						}
					}
				} else {
					response, err = model.Call(t.Context(), request)
				}
				if response != nil || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted || calls != 0 {
					t.Fatalf("response=%v error=%v transport calls=%d", response, err, calls)
				}
			})
		}
	}
}

func TestTranscriptionRejectsCallbackDeliveryBeforeIO(t *testing.T) {
	for _, field := range []string{"callback", "callback_method"} {
		t.Run(field, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls++
				writer.Header().Set("Content-Type", "application/json")
				if request.URL.Query().Has("callback") {
					fmt.Fprint(writer, `{"request_id":"async-transcription"}`)
					return
				}
				fmt.Fprint(writer, `{"results":{"channels":[{"alternatives":[{"transcript":"transcription"}]}]}}`)
			}))
			t.Cleanup(server.Close)
			options := transcription.Options{Model: "nova-3"}
			value := "https://callback.test/result"
			if field == "callback_method" {
				value = http.MethodPut
			}
			if err := options.Extensions.Set(deepgram.TranscriptionRequestExtensionKey, map[string]any{"extra": url.Values{field: {value}}}); err != nil {
				t.Fatal(err)
			}
			model, err := deepgram.NewTranscriptionModel(t.Context(), deepgram.TranscriptionModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: options})
			if err != nil {
				t.Fatal(err)
			}
			audio, err := media.NewBytes("audio/wav", []byte("audio"))
			if err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(t.Context(), &transcription.Request{Audio: audio})
			if response != nil || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted || calls != 0 {
				t.Fatalf("response=%v error=%v transport calls=%d", response, err, calls)
			}
		})
	}
}
