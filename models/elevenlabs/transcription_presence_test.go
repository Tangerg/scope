package elevenlabs_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/elevenlabs"
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

			server := modeltest.JSONServer(http.StatusOK, `{`+field+`}`)
			defer server.Close()
			model, err := elevenlabs.NewTranscriptionModel(t.Context(), elevenlabs.TranscriptionModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: transcription.Options{Model: "scribe_v2"}})
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
			for _, key := range []string{"elevenlabs/confidence", "elevenlabs/language_probability"} {
				if _, exists := response.Output.Metadata[key]; exists {
					t.Fatalf("unreported field %s present", key)
				}
			}
			for _, key := range []string{"elevenlabs/duration_seconds", "elevenlabs/audio_duration_seconds", "elevenlabs/channels"} {
				if _, exists := response.Metadata.Extra[key]; exists {
					t.Fatalf("unreported field %s present", key)
				}
			}
		})
	}
}
