package elevenlabs_test

import (
	"testing"

	"github.com/Tangerg/scope/core/modeltest"
	tts "github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/elevenlabs"
)

func TestAudioTTSModel_Call_Mock(t *testing.T) {
	// ElevenLabs returns raw audio bytes from /text-to-speech.
	srv := modeltest.BinaryServer(200, "audio/mpeg", []byte("FAKE-MP3"))
	t.Cleanup(srv.Close)

	opts := tts.Options{Model: "eleven_v3"}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	opts.Voice = "voice-id-test"

	m, err := elevenlabs.NewAudioTTSModel(t.Context(), elevenlabs.AudioTTSModelConfig{
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
}
