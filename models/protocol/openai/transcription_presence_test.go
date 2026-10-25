package openai_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/protocol/openai"
)

func TestAudioTextRequiresReportedText(t *testing.T) {
	for _, translation := range []bool{false, true} {
		for _, body := range []string{`null`, `{}`, `{"text":null}`, `{"text":""}`} {
			t.Run(body, func(t *testing.T) {
				server := modeltest.JSONServer(http.StatusOK, body)
				defer server.Close()
				var model transcription.Model
				var err error
				if translation {
					model, err = openai.NewAudioTranslationModel(t.Context(), openai.AudioTranslationModelConfig{Provider: "openai", APIKey: "test", BaseURL: server.URL, DefaultOptions: transcription.Options{Model: "whisper-1"}})
				} else {
					model, err = openai.NewTranscriptionModel(t.Context(), openai.TranscriptionModelConfig{Provider: "openai", APIKey: "test", BaseURL: server.URL, DefaultOptions: transcription.Options{Model: "whisper-1"}})
				}
				if err != nil {
					t.Fatal(err)
				}
				audio, err := media.NewBytes("audio/mpeg", []byte("AUDIO"))
				if err != nil {
					t.Fatal(err)
				}
				request, err := transcription.NewRequest(audio)
				if err != nil {
					t.Fatal(err)
				}
				response, err := model.Call(t.Context(), request)
				if body != `{"text":""}` {
					if !errors.Is(err, transcription.ErrInvalidResponse) {
						t.Fatalf("response=%v error=%v", response, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if response.Output.Text != "" {
					t.Fatalf("text=%q", response.Output.Text)
				}
			})
		}
	}
}
