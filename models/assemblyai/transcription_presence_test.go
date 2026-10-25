package assemblyai_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/assemblyai"
)

func TestTranscriptionRequiresReportedText(t *testing.T) {
	for _, test := range []struct {
		name, field string
		valid       bool
	}{
		{"missing", "", false}, {"null", `"text":null`, false}, {"empty", `"text":""`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			field := test.field
			if field != "" {
				field += ","
			}

			server := modeltest.JSONServer(http.StatusOK, `{`+field+`"id":"job","status":"completed"}`)
			defer server.Close()
			model, err := assemblyai.NewTranscriptionModel(t.Context(), assemblyai.TranscriptionModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: transcription.Options{Model: assemblyai.ModelUniversal3Point5Pro}})
			if err != nil {
				t.Fatal(err)
			}
			audio, err := media.NewURI("audio/mpeg", "https://example.com/audio.mp3")
			if err != nil {
				t.Fatal(err)
			}
			request, err := transcription.NewRequest(audio)
			if err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(t.Context(), request)
			if !test.valid {
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
			for _, key := range []string{"assemblyai/confidence", "assemblyai/language_probability"} {
				if _, exists := response.Output.Metadata[key]; exists {
					t.Fatalf("unreported field %s present", key)
				}
			}
			for _, key := range []string{"assemblyai/duration_seconds", "assemblyai/audio_duration_seconds", "assemblyai/channels"} {
				if _, exists := response.Metadata.Extra[key]; exists {
					t.Fatalf("unreported field %s present", key)
				}
			}
		})
	}
}
