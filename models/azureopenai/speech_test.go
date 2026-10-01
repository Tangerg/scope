package azureopenai_test

import (
	"testing"

	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/azureopenai"
)

func TestSpeechModel_Call_Mock(t *testing.T) {
	srv := modeltest.BinaryServer(200, "audio/mpeg", []byte("FAKE-MP3"))
	t.Cleanup(srv.Close)

	opts := speech.Options{Model: "tts-1-deployment"}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	opts.Voice = "alloy"
	m, err := azureopenai.NewSpeechModel(t.Context(), azureopenai.SpeechModelConfig{
		Config:         azureopenai.Config{APIKey: "test-key", BaseURL: srv.URL + "/openai/v1/"},
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
