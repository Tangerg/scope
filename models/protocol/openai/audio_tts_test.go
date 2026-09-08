package openai_test

import (
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/modeltest"
	tts "github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/protocol/openai"
)

func TestAudioTTSModel_Call_Mock(t *testing.T) {
	// OpenAI TTS returns raw audio bytes (not JSON).
	canned := []byte("FAKE-AUDIO-BYTES-FOR-TEST")
	srv := modeltest.BinaryServer(200, "audio/mpeg", canned)
	t.Cleanup(srv.Close)

	opts := tts.Options{Model: "tts-1"}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	opts.Voice = "alloy"
	m, err := openai.NewAudioTTSModel(t.Context(), openai.AudioTTSModelConfig{
		Provider:       "openai",
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

	limited, err := openai.NewAudioTTSModel(t.Context(), openai.AudioTTSModelConfig{
		Provider:         "openai",
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
	opts := tts.Options{Model: "tts-1"}
	_, err := openai.NewAudioTTSModel(t.Context(), openai.AudioTTSModelConfig{
		Provider: "openai", APIKey: "test-key", DefaultOptions: opts, MaxResponseBytes: -1,
	})
	if err == nil {
		t.Fatal("NewAudioTTSModel accepted a negative response limit")
	}
}
