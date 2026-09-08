package elevenlabs_test

import (
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/elevenlabs"
)

const elevenSTTJSON = `{
  "language_code": "en",
  "language_probability": 0.99,
  "text": "hello world",
  "words": []
}`

func TestAudioTranscriptionModel_Call_Mock(t *testing.T) {
	srv := modeltest.JSONServer(http.StatusOK, elevenSTTJSON)
	t.Cleanup(srv.Close)

	opts := transcription.Options{Model: elevenlabs.ModelScribeV2}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	m, err := elevenlabs.NewAudioTranscriptionModel(t.Context(), elevenlabs.AudioTranscriptionModelConfig{
		APIKey:         "test-key",
		DefaultOptions: opts,
		BaseURL:        srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	audio, _ := media.NewBytes("audio/mpeg", []byte("FAKE-AUDIO"))
	req, _ := transcription.NewRequest(audio)
	out, err := m.Call(t.Context(), req)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out.Output == nil || out.Output.Text != "hello world" {
		t.Fatalf("text = %q; want 'hello world'", out.Output.Text)
	}
}
