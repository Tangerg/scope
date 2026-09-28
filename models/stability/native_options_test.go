package stability_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/models/stability"
)

func TestOfficialImageOptionsReachForm(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
		}
		for field, want := range map[string]string{"aspect_ratio": "16:9", "style_preset": "photographic", "cfg_scale": "7"} {
			if got := r.FormValue(field); got != want {
				t.Errorf("%s=%q want %q", field, got, want)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"image":"aW1hZ2U=","finish_reason":"SUCCESS"}`)
	}))
	defer server.Close()
	opts := image.Options{Model: stability.ModelCore}
	if err := opts.Extensions.Set(stability.RequestExtensionKey, map[string]any{"aspect_ratio": "16:9", "style_preset": "photographic", "cfg_scale": 7}); err != nil {
		t.Fatal(err)
	}
	model, err := stability.NewImageModel(t.Context(), stability.ImageModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: opts})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := model.Call(t.Context(), &image.Request{Prompt: "landscape"}); err != nil {
		t.Fatal(err)
	}
}
