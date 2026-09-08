package azureopenai_test

import (
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/azureopenai"
)

func TestAudioTranscriptionModel_Call_Mock(t *testing.T) {
	srv := modeltest.JSONServer(http.StatusOK, `{"text":"hello world"}`)
	t.Cleanup(srv.Close)

	opts := transcription.Options{Model: "whisper-deployment"}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	m, err := azureopenai.NewAudioTranscriptionModel(t.Context(), azureopenai.AudioTranscriptionModelConfig{
		Config:         azureopenai.Config{APIKey: "test-key", BaseURL: srv.URL + "/openai/v1/"},
		DefaultOptions: opts,
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
		t.Fatalf("text = %v; want 'hello world'", out.Output)
	}
}
