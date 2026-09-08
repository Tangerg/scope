package azureopenai_test

import (
	"testing"

	"github.com/Tangerg/scope/core/modeltest"
	tts "github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/azureopenai"
)

func TestAudioTTSModel_Call_Mock(t *testing.T) {
	srv := modeltest.BinaryServer(200, "audio/mpeg", []byte("FAKE-MP3"))
	t.Cleanup(srv.Close)

	opts := tts.Options{Model: "tts-1-deployment"}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	opts.Voice = "alloy"
	m, err := azureopenai.NewAudioTTSModel(t.Context(), azureopenai.AudioTTSModelConfig{
		Config:         azureopenai.Config{APIKey: "test-key", BaseURL: srv.URL + "/openai/v1/"},
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
