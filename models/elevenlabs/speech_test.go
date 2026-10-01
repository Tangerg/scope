package elevenlabs_test

import (
	"testing"

	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/elevenlabs"
)

func TestSpeechModel_Call_Mock(t *testing.T) {
	// ElevenLabs returns raw audio bytes from /text-to-speech.
	srv := modeltest.BinaryServer(200, "audio/mpeg", []byte("FAKE-MP3"))
	t.Cleanup(srv.Close)

	opts := speech.Options{Model: "eleven_v3"}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	opts.Voice = "voice-id-test"

	m, err := elevenlabs.NewSpeechModel(t.Context(), elevenlabs.SpeechModelConfig{
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
}
