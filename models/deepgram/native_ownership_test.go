package deepgram_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/deepgram"
)

func TestTranscriptionRejectsNativeCoreFieldsBeforeIO(t *testing.T) {
	for _, field := range []string{"model", "language"} {
		for _, defaults := range []bool{false, true} {
			t.Run(field+map[bool]string{false: "/request", true: "/defaults"}[defaults], func(t *testing.T) {
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					requests++
					writer.WriteHeader(http.StatusBadRequest)
				}))
				defer server.Close()
				var extensions metadata.Extensions
				if err := extensions.Set(deepgram.TranscriptionRequestExtensionKey, map[string]any{field: nil}); err != nil {
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
				model, err := deepgram.NewAudioTranscriptionModel(t.Context(), deepgram.AudioTranscriptionModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: options})
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
