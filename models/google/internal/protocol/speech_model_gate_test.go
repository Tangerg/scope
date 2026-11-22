package protocol

import (
	"testing"

	"github.com/Tangerg/scope/core/speech"
)

// The unary and streaming models serve disjoint model families, so a request
// override must pass the same gate as the defaults.
func TestSpeechModelGateAppliesToRequestOverrides(t *testing.T) {
	config := SpeechModelConfig{Provider: GoogleProvider, Client: ClientConfig{APIKey: "key"}, DefaultOptions: speech.Options{Model: ModelGemini25FlashPreviewTTS}}
	unary, err := NewSpeechModel(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	override := &speech.Request{Text: "hello", Options: speech.Options{Model: ModelGemini31FlashTTSPreview}}
	if _, _, _, err = unary.binding.buildAPITTSRequest(override); err == nil {
		t.Error("unary speech admitted a streaming-only model override")
	}

	config.DefaultOptions.Model = ModelGemini31FlashTTSPreview
	streaming, err := NewStreamingSpeechModel(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	override.Options.Model = ModelGemini25FlashPreviewTTS
	if _, _, _, err = streaming.binding.buildAPITTSRequest(override); err == nil {
		t.Error("streaming speech admitted a unary-only model override")
	}
}
