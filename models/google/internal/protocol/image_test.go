package protocol_test

import (
	"encoding/base64"
	"net/http"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/google/internal/protocol"
)

func TestImageModelCallUsesInteractionsAPI(t *testing.T) {
	first := base64.StdEncoding.EncodeToString([]byte("FIRST"))
	second := base64.StdEncoding.EncodeToString([]byte("SECOND"))
	body := `{
		"id":"int_123",
		"model":"gemini-3.1-flash-image",
		"status":"completed",
		"object":"interaction",
		"created":"2026-07-31T10:20:30Z",
		"steps":[
			{"type":"thought","summary":[{"type":"image","data":"` + first + `","mime_type":"image/png"}]},
			{"type":"model_output","content":[
				{"type":"text","text":"done"},
				{"type":"image","data":"` + first + `","mime_type":"image/png"},
				{"type":"image","data":"` + second + `","mime_type":"image/jpeg"}
			]}
		]
	}`
	srv := modeltest.JSONServer(http.StatusOK, body)
	t.Cleanup(srv.Close)

	opts := image.Options{Model: protocol.ModelGemini31FlashImage}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	opts.OutputFormat = "image/jpeg"
	if setExtensionErr := opts.Extensions.Set(protocol.ImageRequestExtensionKey, protocol.ImageGenerationOptions{
		AspectRatio: "16:9",
		ImageSize:   "2K",
	}); setExtensionErr != nil {
		t.Fatal(setExtensionErr)
	}
	model, err := protocol.NewImageModel(t.Context(), protocol.ImageModelConfig{
		Client:         protocol.ClientConfig{APIKey: "test-key", BaseURL: srv.URL},
		DefaultOptions: opts,
	})
	if err != nil {
		t.Fatal(err)
	}

	req, _ := image.NewRequest("a serene mountain lake")
	out, err := model.Call(t.Context(), req)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(out.Outputs) != 2 || out.First() == nil {
		t.Fatalf("outputs = %#v", out.Outputs)
	}
	if out.Outputs[1].Media.MIME != "image/jpeg" {
		t.Fatalf("second MIME = %q", out.Outputs[1].Media.MIME)
	}
	if got := out.Metadata.CreatedAt; !got.Equal(time.Date(2026, 7, 31, 10, 20, 30, 0, time.UTC)) {
		t.Fatalf("CreatedAt = %s", got)
	}
	if _, ok, err := out.Metadata.Extra.Decode[map[string]any](protocol.ImageResponseExtensionKey); err != nil || !ok {
		t.Fatalf("native response extension: exists=%t err=%v", ok, err)
	}

	hugeSeed := int64(1 << 31)
	req.Options = image.Options{Seed: &hugeSeed}
	if _, err := model.Call(t.Context(), req); err == nil {
		t.Fatal("Call accepted a seed that overflows the provider wire type")
	}
}

func TestImageModelRejectsUnsupportedImagenOnlyOptions(t *testing.T) {
	opts := image.Options{Model: protocol.ModelGemini31FlashImage}
	model, err := protocol.NewImageModel(t.Context(), protocol.ImageModelConfig{
		Client:         protocol.ClientConfig{APIKey: "test-key", BaseURL: "https://example.com"},
		DefaultOptions: opts,
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := image.NewRequest("a scope")
	req.Options.NegativePrompt = "blurry"
	if _, err := model.Call(t.Context(), req); err == nil {
		t.Fatal("Call accepted Imagen-only negative_prompt")
	}
}
