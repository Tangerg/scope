package protocol_test

import (
	"encoding/base64"
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/modeltest"
	tts "github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/google/internal/protocol"
)

func TestAudioTTSModel_Call_Mock(t *testing.T) {
	// Gemini TTS routes audio through GenerateContent with inline
	// data — Part.inlineData.{mimeType, data} carries the PCM bytes.
	audioB64 := base64.StdEncoding.EncodeToString([]byte("FAKE-PCM"))
	body := `{
  "candidates": [{
    "content": {"role": "model", "parts": [{"inlineData": {"mimeType": "audio/L16;rate=24000", "data": "` + audioB64 + `"}}]},
    "finishReason": "STOP"
  }],
  "usageMetadata": {"promptTokenCount": 4, "candidatesTokenCount": 0, "totalTokenCount": 4}
}`
	srv := modeltest.JSONServer(http.StatusOK, body)
	t.Cleanup(srv.Close)

	opts := tts.Options{Model: protocol.ModelGemini31FlashTTSPreview}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	m, err := protocol.NewAudioTTSModel(t.Context(), protocol.AudioTTSModelConfig{
		Provider:       "google",
		Client:         protocol.ClientConfig{APIKey: "test-key", BaseURL: srv.URL},
		DefaultOptions: opts,
	})
	if err != nil {
		t.Fatal(err)
	}

	req, _ := tts.NewRequest("hello world")
	out, err := m.Call(t.Context(), req)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out.Output == nil {
		t.Fatal("nil output")
	}
}
