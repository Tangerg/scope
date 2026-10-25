package deepgram_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/deepgram"
)

func TestTranscriptionRequiresReportedText(t *testing.T) {
	for _, test := range []struct {
		name, field string
		valid       bool
	}{
		{"missing", "", false}, {"null", `"transcript":null`, false}, {"empty", `"transcript":""`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			field := test.field

			server := modeltest.JSONServer(http.StatusOK, `{"results":{"channels":[{"alternatives":[{`+field+`}]}]}}`)
			defer server.Close()
			model, err := deepgram.NewTranscriptionModel(t.Context(), deepgram.TranscriptionModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: transcription.Options{Model: "nova-3"}})
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
			for _, key := range []string{"deepgram/confidence", "deepgram/language_probability"} {
				if _, exists := response.Output.Metadata[key]; exists {
					t.Fatalf("unreported field %s present", key)
				}
			}
			for _, key := range []string{"deepgram/duration_seconds", "deepgram/audio_duration_seconds", "deepgram/channels"} {
				if _, exists := response.Metadata.Extra[key]; exists {
					t.Fatalf("unreported field %s present", key)
				}
			}
		})
	}
}
