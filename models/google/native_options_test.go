package google_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/google"
	"github.com/Tangerg/scope/models/google/vertexai"
)

func TestNativeMediaRejectsIgnoredControlsBeforeIO(t *testing.T) {
	for _, provider := range []string{"google", "vertexai"} {
		for _, test := range []struct{ capability, options string }{
			{"speech", `{"temperature_value":1}`},
			{"speech", `{"responseModalities":["TEXT"]}`},
			{"speech", `{"speechConfig":{"voiceConfig":null}}`},
			{"speech", `null`},
			{"transcription", `{"temperature_value":1}`},
			{"transcription", `null`},
			{"image", `{"seed":1}`},
			{"image", `null`},
		} {
			t.Run(provider+"/"+test.capability+"/"+test.options, func(t *testing.T) {
				var calls atomic.Int64
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
					calls.Add(1)
					writer.WriteHeader(http.StatusBadRequest)
				}))
				defer server.Close()
				var extensions metadata.Extensions
				if err := extensions.Set(provider+"/"+test.capability+"_request", json.RawMessage(test.options)); err != nil {
					t.Fatal(err)
				}
				var err error
				switch test.capability {
				case "speech":
					_, err = newNativeUnarySpeech(t, provider, server).Call(t.Context(), &speech.Request{Text: "hello", Options: speech.Options{Extensions: extensions}})
				case "transcription":
					request := nativeTranscriptionRequest(t)
					request.Options.Extensions = extensions
					_, err = newNativeTranscription(t, provider, server).Call(t.Context(), request)
				case "image":
					var model image.Model
					if provider == "google" {
						model, err = google.NewImageModel(t.Context(), google.ImageModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: image.Options{Model: google.ModelGemini31FlashImage}})
					} else {
						model, err = vertexai.NewImageModel(t.Context(), vertexai.ImageModelConfig{
							Client:         vertexai.ClientConfig{Project: "project", Location: "global", BaseURL: server.URL, HTTPClient: server.Client()},
							DefaultOptions: image.Options{Model: vertexai.ModelGemini3ProImage},
						})
					}
					if err != nil {
						t.Fatal(err)
					}
					_, err = model.Call(t.Context(), &image.Request{Prompt: "image", Options: image.Options{Extensions: extensions}})
				}
				if err == nil || calls.Load() != 0 {
					t.Fatalf("error=%v provider calls=%d", err, calls.Load())
				}
			})
		}
	}
}
