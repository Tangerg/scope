package prodia_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/models/prodia"
)

func TestImagePreservesNativeConfigIntegerPrecision(t *testing.T) {
	var captured string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured = string(body)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("PNG"))
	}))
	defer server.Close()

	options := image.Options{Model: "inference.flux.dev.txt2img.v1"}
	if err := options.Extensions.Set(prodia.ImageRequestExtensionKey, json.RawMessage(`{"config":{"clip_skip_seed":9007199254740993}}`)); err != nil {
		t.Fatal(err)
	}
	model, err := prodia.NewImageModel(t.Context(), prodia.ImageModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: options})
	if err != nil {
		t.Fatal(err)
	}
	request, err := image.NewRequest("hello")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := model.Call(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(captured, "9007199254740993") || strings.Contains(captured, "9007199254740992") {
		t.Fatalf("serialized request lost integer precision: %s", captured)
	}
}

func TestImageRejectsNativeCoreFieldsBeforeIO(t *testing.T) {
	for _, field := range []string{"type", "config.prompt", "config.negative_prompt", "config.width", "config.height", "config.seed"} {
		for _, defaults := range []bool{false, true} {
			t.Run(field+map[bool]string{false: "/request", true: "/defaults"}[defaults], func(t *testing.T) {
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					requests++
					writer.WriteHeader(http.StatusBadRequest)
				}))
				defer server.Close()
				native := map[string]any{field: nil}
				if strings.HasPrefix(field, "config.") {
					native = map[string]any{"config": map[string]any{strings.TrimPrefix(field, "config."): nil}}
				}
				var extensions metadata.Extensions
				if err := extensions.Set(prodia.ImageRequestExtensionKey, native); err != nil {
					t.Fatal(err)
				}
				options := image.Options{Model: "inference.flux.dev.txt2img.v1"}
				request, err := image.NewRequest("current prompt")
				if err != nil {
					t.Fatal(err)
				}
				if defaults {
					options.Extensions = extensions
				} else {
					request.Options.Extensions = extensions
				}
				model, err := prodia.NewImageModel(t.Context(), prodia.ImageModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: options})
				if err != nil {
					t.Fatal(err)
				}
				_, err = model.Call(t.Context(), request)
				if err == nil || !strings.Contains(err.Error(), "owned by Core") || requests != 0 {
					t.Fatalf("native field accepted: requests=%d err=%v", requests, err)
				}
			})
		}
	}
}
