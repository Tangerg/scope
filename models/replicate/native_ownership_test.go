package replicate_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/replicate"
)

func TestImagePreservesNativeInputIntegerPrecision(t *testing.T) {
	var captured string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			captured = string(body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"x","status":"failed","error":"stop"}`))
	}))
	defer server.Close()

	opts := image.Options{Model: "owner/model"}
	if err := opts.Extensions.Set(replicate.ImageRequestExtensionKey, json.RawMessage(`{"input":{"secondary_seed":9007199254740993}}`)); err != nil {
		t.Fatal(err)
	}
	schema := replicate.ImageInputSchema{PromptKey: "prompt", OutputKind: replicate.FileOutputURI}
	model, err := replicate.NewImageModel(t.Context(), replicate.ImageModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: opts, InputSchema: schema})
	if err != nil {
		t.Fatal(err)
	}
	// The Call fails on the stubbed prediction; only the serialized request matters.
	_, _ = model.Call(t.Context(), &image.Request{Prompt: "hello"})
	if !strings.Contains(captured, "9007199254740993") || strings.Contains(captured, "9007199254740992") {
		t.Fatalf("serialized request lost integer precision: %s", captured)
	}
}

func TestImageRejectsBoundNativeInputBeforeIO(t *testing.T) {
	for _, field := range []string{"prompt", "negative", "width", "height", "seed", "format"} {
		t.Run(field, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusBadRequest) }))
			defer server.Close()
			opts := image.Options{Model: "owner/model"}
			if err := opts.Extensions.Set(replicate.ImageRequestExtensionKey, map[string]any{"input": map[string]any{field: nil}}); err != nil {
				t.Fatal(err)
			}
			schema := replicate.ImageInputSchema{PromptKey: "prompt", NegativePromptKey: "negative", WidthKey: "width", HeightKey: "height", SeedKey: "seed", OutputFormatKey: "format", OutputFormats: map[string]string{"image/png": "png"}, OutputKind: replicate.FileOutputURI}
			model, err := replicate.NewImageModel(t.Context(), replicate.ImageModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: opts, InputSchema: schema})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := model.Call(t.Context(), &image.Request{Prompt: "hello"}); err == nil || !strings.Contains(err.Error(), "owned by Core") || calls != 0 {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
		})
	}
}
func TestSpeechRejectsBoundNativeInputBeforeIO(t *testing.T) {
	for _, field := range []string{"text", "speaker", "speed"} {
		t.Run(field, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusBadRequest) }))
			defer server.Close()
			opts := speech.Options{Model: "owner/model"}
			if err := opts.Extensions.Set(replicate.SpeechRequestExtensionKey, map[string]any{"input": map[string]any{field: "other"}}); err != nil {
				t.Fatal(err)
			}
			schema := replicate.SpeechInputSchema{TextKey: "text", VoiceKey: "speaker", SpeedKey: "speed", VoiceRequired: true, OutputKind: replicate.FileOutputURI}
			model, err := replicate.NewSpeechModel(t.Context(), replicate.SpeechModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: opts, InputSchema: schema})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := model.Call(t.Context(), &speech.Request{Text: "hello"}); err == nil || !strings.Contains(err.Error(), "owned by Core") || calls != 0 {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
		})
	}
}
