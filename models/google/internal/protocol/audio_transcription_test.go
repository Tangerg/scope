package protocol_test

import (
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/google/internal/protocol"
)

func TestAudioTranscriptionModel_Call_Mock(t *testing.T) {
	// Gemini transcription is multimodal chat: prompt + audio → text.
	body := `{
  "candidates": [{
    "content": {"role": "model", "parts": [{"text": "hello world"}]},
    "finishReason": "STOP"
  }],
  "usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 2, "totalTokenCount": 12}
}`
	srv := modeltest.JSONServer(http.StatusOK, body)
	t.Cleanup(srv.Close)

	opts := transcription.Options{Model: protocol.ModelGemini36Flash}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	m, err := protocol.NewAudioTranscriptionModel(t.Context(), protocol.AudioTranscriptionModelConfig{
		Provider:       "google",
		Client:         protocol.ClientConfig{APIKey: "test-key", BaseURL: srv.URL},
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
