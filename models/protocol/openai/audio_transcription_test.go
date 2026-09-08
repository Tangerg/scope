package openai_test

import (
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/protocol/openai"
)

func TestAudioTextModels_Call_Mock(t *testing.T) {
	opts := transcription.Options{Model: "whisper-1"}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		response string
		want     string
		newModel func(string) (transcription.Model, error)
	}{
		{
			name:     "transcription",
			response: `{"text":"hello world"}`,
			want:     "hello world",
			newModel: func(baseURL string) (transcription.Model, error) {
				return openai.NewAudioTranscriptionModel(t.Context(), openai.AudioTranscriptionModelConfig{
					Provider: "openai", APIKey: "test-key", DefaultOptions: opts, BaseURL: baseURL,
				})
			},
		},
		{
			name:     "translation",
			response: `{"text":"good morning"}`,
			want:     "good morning",
			newModel: func(baseURL string) (transcription.Model, error) {
				return openai.NewAudioTranslationModel(t.Context(), openai.AudioTranslationModelConfig{
					Provider: "openai", APIKey: "test-key", DefaultOptions: opts, BaseURL: baseURL,
				})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv := modeltest.JSONServer(http.StatusOK, test.response)
			t.Cleanup(srv.Close)
			model, err := test.newModel(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			audio, err := media.NewBytes("audio/mpeg", []byte("FAKE-AUDIO"))
			if err != nil {
				t.Fatal(err)
			}
			request, err := transcription.NewRequest(audio)
			if err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(t.Context(), request)
			if err != nil {
				t.Fatalf("Call: %v", err)
			}
			if response.Output == nil || response.Output.Text != test.want {
				t.Fatalf("text = %v; want %q", response.Output, test.want)
			}
		})
	}
}
