package deepgram

import (
	"math"
	"testing"

	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/core/transcription"
)

func TestResponseLimitFitsHTTPAdapterOn386(t *testing.T) {
	adapter, err := newAPI(t.Context(), apiConfig{APIKey: "test-key", MaxResponseBytes: math.MaxInt32})
	if err != nil {
		t.Fatal(err)
	}
	if adapter.maxResponseBytes != math.MaxInt32 || adapter.http.ResponseBodyLimit != math.MaxInt32 {
		t.Fatalf("largest admissible limit changed: speech=%d transcription=%d", adapter.maxResponseBytes, adapter.http.ResponseBodyLimit)
	}

	const overflowLimit = int64(math.MaxInt32) + 1
	config := apiConfig{APIKey: "test-key", MaxResponseBytes: overflowLimit}
	if err := config.validate(); err == nil {
		t.Fatal("API configuration admitted an unrepresentable HTTP response limit")
	}
	if adapter, err := newAPI(t.Context(), config); adapter != nil || err == nil {
		t.Fatalf("API construction admitted an unrepresentable HTTP response limit: adapter=%v error=%v", adapter, err)
	}
	if err := (SpeechModelConfig{
		APIKey:           "test-key",
		DefaultOptions:   speech.Options{Model: "aura-asteria-en"},
		MaxResponseBytes: overflowLimit,
	}).Validate(); err == nil {
		t.Fatal("speech configuration admitted an unrepresentable HTTP response limit")
	}
	if err := (TranscriptionModelConfig{
		APIKey:           "test-key",
		DefaultOptions:   transcription.Options{Model: "nova-3"},
		MaxResponseBytes: overflowLimit,
	}).Validate(); err == nil {
		t.Fatal("transcription configuration admitted an unrepresentable HTTP response limit")
	}
}
