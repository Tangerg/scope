package deepgram_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/modeltest"
	tts "github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/deepgram"
)

func TestAudioTTSModel_Call_Mock(t *testing.T) {
	// Deepgram /speak returns raw audio bytes.
	srv := modeltest.MuxServer(modeltest.Route{Method: "POST", Contains: "/speak", Handle: func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("encoding") != "linear16" || query.Get("container") != "wav" || query.Get("speed") != "1.2" {
			t.Errorf("query = %v", query)
		}
		w.Header().Set("Content-Type", "audio/wav")
		w.Write([]byte("FAKE-WAV"))
	}})
	t.Cleanup(srv.Close)

	opts := tts.Options{Model: "aura-asteria-en"}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	opts.OutputFormat = "wav"
	opts.Speed = 1.2
	m, err := deepgram.NewAudioTTSModel(t.Context(), deepgram.AudioTTSModelConfig{
		APIKey:         "test-key",
		DefaultOptions: opts,
		BaseURL:        srv.URL,
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

	limited, err := deepgram.NewAudioTTSModel(t.Context(), deepgram.AudioTTSModelConfig{
		APIKey:           "test-key",
		DefaultOptions:   opts,
		BaseURL:          srv.URL,
		MaxResponseBytes: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = limited.Call(t.Context(), req); err == nil || !strings.Contains(err.Error(), "4-byte limit") {
		t.Fatalf("limited Call error = %v", err)
	}
}

func TestAudioTTSModelConfigRejectsNegativeResponseLimit(t *testing.T) {
	opts := tts.Options{Model: "aura-asteria-en"}
	_, err := deepgram.NewAudioTTSModel(t.Context(), deepgram.AudioTTSModelConfig{
		APIKey: "test-key", DefaultOptions: opts, MaxResponseBytes: -1,
	})
	if err == nil {
		t.Fatal("NewAudioTTSModel accepted a negative response limit")
	}
}
