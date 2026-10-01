package openai_test

import (
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/protocol/openai"
)

func TestSpeechModel_Call_Mock(t *testing.T) {
	// OpenAI TTS returns raw audio bytes (not JSON).
	canned := []byte("FAKE-AUDIO-BYTES-FOR-TEST")
	srv := modeltest.BinaryServer(200, "audio/mpeg", canned)
	t.Cleanup(srv.Close)

	opts := speech.Options{Model: "tts-1"}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	opts.Voice = "alloy"
	m, err := openai.NewSpeechModel(t.Context(), openai.SpeechModelConfig{
		Provider:       "openai",
		APIKey:         "test-key",
		DefaultOptions: opts,
		BaseURL:        srv.URL,
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

	limited, err := openai.NewSpeechModel(t.Context(), openai.SpeechModelConfig{
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

func TestSpeechModelConfigRejectsNegativeResponseLimit(t *testing.T) {
	opts := speech.Options{Model: "tts-1"}
	_, err := openai.NewSpeechModel(t.Context(), openai.SpeechModelConfig{
		Provider: "openai", APIKey: "test-key", DefaultOptions: opts, MaxResponseBytes: -1,
	})
	if err == nil {
		t.Fatal("NewSpeechModel accepted a negative response limit")
	}
}
