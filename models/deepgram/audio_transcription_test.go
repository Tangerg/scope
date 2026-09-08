package deepgram_test

import (
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/deepgram"
)

// Deepgram /listen response shape (simplified — full payload has
// channels/alternatives/words/etc.; the package picks out the top
// transcript via the standard JSON path).
const deepgramSTTJSON = `{
  "metadata": {"request_id":"abc","model_info":{"model-uuid":{"name":"nova-3","tier":"nova"}}},
  "results": {
    "channels": [{
      "alternatives": [{
        "transcript": "hello world",
        "confidence": 0.99,
        "words": []
      }]
    }]
  }
}`

func TestAudioTranscriptionModel_Call_Mock(t *testing.T) {
	srv := modeltest.JSONServer(http.StatusOK, deepgramSTTJSON)
	t.Cleanup(srv.Close)

	opts := transcription.Options{Model: "nova-3"}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	m, err := deepgram.NewAudioTranscriptionModel(t.Context(), deepgram.AudioTranscriptionModelConfig{
		APIKey:         "test-key",
		DefaultOptions: opts,
		BaseURL:        srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	audio, err := media.NewBytes("audio/mpeg", []byte("FAKE-AUDIO"))
	if err != nil {
		t.Fatal(err)
	}
	req, err := transcription.NewRequest(audio)
	if err != nil {
		t.Fatal(err)
	}
	out, err := m.Call(t.Context(), req)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out.Output == nil || out.Output.Text == "" {
		t.Fatal("empty transcript")
	}

	limited, err := deepgram.NewAudioTranscriptionModel(t.Context(), deepgram.AudioTranscriptionModelConfig{
		APIKey:           "test-key",
		DefaultOptions:   opts,
		BaseURL:          srv.URL,
		MaxResponseBytes: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = limited.Call(t.Context(), req); err == nil {
		t.Fatal("Call accepted a transcription response over the configured limit")
	}
}
