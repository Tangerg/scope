package revai_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/revai"
)

func TestTranscriptionRejectsNativeCoreFieldsBeforeIO(t *testing.T) {
	for _, field := range []string{"media_url", "language", "transcriber"} {
		for _, defaults := range []bool{false, true} {
			t.Run(field+map[bool]string{false: "/request", true: "/defaults"}[defaults], func(t *testing.T) {
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					requests++
					writer.WriteHeader(http.StatusBadRequest)
				}))
				defer server.Close()
				var extensions metadata.Extensions
				if err := extensions.Set(revai.RequestExtensionKey, map[string]any{field: nil}); err != nil {
					t.Fatal(err)
				}
				options := transcription.Options{Model: "test-model"}
				audio, err := media.NewBytes("audio/mpeg", []byte("audio"))
				if err != nil {
					t.Fatal(err)
				}
				request, err := transcription.NewRequest(audio)
				if err != nil {
					t.Fatal(err)
				}
				if defaults {
					options.Extensions = extensions
				} else {
					request.Options.Extensions = extensions
				}
				model, err := revai.NewTranscriptionModel(t.Context(), revai.TranscriptionModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: options})
				if err != nil {
					t.Fatal(err)
				}
				_, err = model.Call(t.Context(), request)
				if err == nil || !strings.Contains(err.Error(), "owned by Core") || requests != 0 {
					t.Fatalf("native field accepted: requests=%d error=%v", requests, err)
				}
			})
		}
	}
}

func TestAudioSourceCannotBeSuppliedByNativeConfiguration(t *testing.T) {
	for _, source := range []string{"uri", "inline"} {
		for _, defaults := range []bool{false, true} {
			for _, native := range []any{
				map[string]any{"url": "https://example.test/core.wav"},
				map[string]any{"url": "https://example.test/native.wav"},
				map[string]any{"url": nil},
				map[string]any{"unknown": true},
				[]string{"invalid"},
			} {
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(http.StatusBadRequest) }))
				var extensions metadata.Extensions
				if err := extensions.Set(revai.RequestExtensionKey, map[string]any{"source_config": native}); err != nil {
					t.Fatal(err)
				}
				audio, err := media.NewURI("audio/wav", "https://example.test/core.wav")
				if source == "inline" {
					audio, err = media.NewBytes("audio/wav", []byte("audio"))
				}
				if err != nil {
					t.Fatal(err)
				}
				request, err := transcription.NewRequest(audio)
				if err != nil {
					t.Fatal(err)
				}
				options := transcription.Options{Model: revai.ModelMachine}
				if defaults {
					options.Extensions = extensions
				} else {
					request.Options.Extensions = extensions
				}
				model, err := revai.NewTranscriptionModel(t.Context(), revai.TranscriptionModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: options})
				if err != nil {
					t.Fatal(err)
				}
				_, err = model.Call(t.Context(), request)
				server.Close()
				if err == nil || requests != 0 {
					t.Fatalf("source=%s defaults=%t native=%#v requests=%d error=%v", source, defaults, native, requests, err)
				}
			}
		}
	}
}
