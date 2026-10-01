package protocol_test

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/google/internal/protocol"
)

func TestSpeechModel_Call_Mock(t *testing.T) {
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

	opts := speech.Options{Model: protocol.ModelGemini25FlashPreviewTTS}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	m, err := protocol.NewSpeechModel(t.Context(), protocol.SpeechModelConfig{
		Provider:       "google",
		Client:         protocol.ClientConfig{APIKey: "test-key", BaseURL: srv.URL},
		DefaultOptions: opts,
	})
	if err != nil {
		t.Fatal(err)
	}

	req, _ := speech.NewRequest("hello world")
	out, err := m.Call(t.Context(), req)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out.Output == nil {
		t.Fatal("nil output")
	}
}

func TestSpeechCapabilitiesAreSeparate(t *testing.T) {
	if _, ok := any((*protocol.SpeechModel)(nil)).(speech.Streamer); ok {
		t.Fatal("unary synthesis must not expose a streaming protocol")
	}
	if _, ok := any((*protocol.StreamingSpeechModel)(nil)).(speech.Model); !ok {
		t.Fatal("streaming synthesis must support aggregation")
	}
	_, err := protocol.NewStreamingSpeechModel(t.Context(), protocol.SpeechModelConfig{
		Provider: "google", Client: protocol.ClientConfig{APIKey: "test"},
		DefaultOptions: speech.Options{Model: "gemini-2.5-flash-preview-tts"},
	})
	if err == nil {
		t.Fatal("accepted unary-only model for streaming")
	}
}

func TestStreamingSpeechUsesIncrementalEndpoint(t *testing.T) {
	srv := modeltest.MuxServer(modeltest.Route{Method: "POST", Contains: ":streamGenerateContent", Handle: func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, audio := range []string{"first", "second"} {
			fmt.Fprintf(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"inlineData\":{\"mimeType\":\"audio/L16\",\"data\":%q}}]}}]}\n\n", base64.StdEncoding.EncodeToString([]byte(audio)))
		}
		fmt.Fprint(w, "data: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n")
	}})
	t.Cleanup(srv.Close)
	model, err := protocol.NewStreamingSpeechModel(t.Context(), protocol.SpeechModelConfig{
		Provider: "google", Client: protocol.ClientConfig{APIKey: "test", BaseURL: srv.URL},
		DefaultOptions: speech.Options{Model: protocol.ModelGemini31FlashTTSPreview},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := speech.NewRequest("hello")
	var chunks []string
	for response, err := range model.Stream(t.Context(), req) {
		if err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, string(response.Output.Audio))
	}
	if !slices.Equal(chunks, []string{"first", "second"}) {
		t.Fatalf("chunks = %q", chunks)
	}
}
