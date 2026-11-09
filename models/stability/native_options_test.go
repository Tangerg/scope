package stability_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/invocation"
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
	opts := image.Options{Model: stability.ModelSD3Point5Large}
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

func TestImageCFGScaleFollowsResolvedModel(t *testing.T) {
	for _, test := range []struct {
		name     string
		model    string
		override string
		accepted bool
	}{
		{name: "core", model: stability.ModelCore},
		{name: "ultra", model: stability.ModelUltra},
		{name: "sd3", model: stability.ModelSD3Point5Large, accepted: true},
		{name: "override core", model: stability.ModelSD3Point5Large, override: stability.ModelCore},
		{name: "override sd3", model: stability.ModelCore, override: stability.ModelSD3Point5Large, accepted: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Error(err)
				}
				if r.URL.Path != "/stable-image/generate/sd3" || r.FormValue("model") != stability.ModelSD3Point5Large || r.FormValue("cfg_scale") != "7" {
					t.Errorf("cfg_scale request: path=%q model=%q value=%q", r.URL.Path, r.FormValue("model"), r.FormValue("cfg_scale"))
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"image":"aW1hZ2U=","finish_reason":"SUCCESS"}`)
			}))
			defer server.Close()
			options := image.Options{Model: test.model}
			if err := options.Extensions.Set(stability.RequestExtensionKey, map[string]any{"cfg_scale": 7}); err != nil {
				t.Fatal(err)
			}
			model, err := stability.NewImageModel(t.Context(), stability.ImageModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: options})
			if err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(t.Context(), &image.Request{Prompt: "landscape", Options: image.Options{Model: test.override}})
			if test.accepted {
				if err != nil || response == nil || calls.Load() != 1 {
					t.Fatalf("supported request: response=%v calls=%d error=%v", response, calls.Load(), err)
				}
			} else if response != nil || calls.Load() != 0 || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted {
				t.Fatalf("unsupported request reached transport: response=%v calls=%d error=%v", response, calls.Load(), err)
			}
		})
	}
}

func TestImageStylePresetAdmission(t *testing.T) {
	for _, modelName := range []string{stability.ModelCore, stability.ModelUltra, stability.ModelSD3Point5Large} {
		for _, preset := range []string{
			"", "enhance", "anime", "photographic", "digital-art", "comic-book", "fantasy-art",
			"line-art", "analog-film", "neon-punk", "isometric", "low-poly", "origami",
			"modeling-compound", "cinematic", "3d-model", "pixel-art", "tile-texture", "unknown",
		} {
			t.Run(modelName+"/"+preset, func(t *testing.T) {
				var calls atomic.Int64
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if err := r.ParseMultipartForm(1 << 20); err != nil {
						t.Error(err)
						return
					}
					_, present := r.MultipartForm.Value["style_preset"]
					if present != (preset != "") || r.FormValue("style_preset") != preset {
						t.Errorf("style_preset=%q present=%t", r.FormValue("style_preset"), present)
					}
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"image":"aW1hZ2U=","finish_reason":"SUCCESS"}`)
				}))
				defer server.Close()
				model, err := stability.NewImageModel(t.Context(), stability.ImageModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: image.Options{Model: modelName}})
				if err != nil {
					t.Fatal(err)
				}
				request := &image.Request{Prompt: "landscape"}
				if setErr := request.Options.Extensions.Set(stability.RequestExtensionKey, map[string]any{"style_preset": preset}); setErr != nil {
					t.Fatal(setErr)
				}
				response, err := model.Call(t.Context(), request)
				if preset == "unknown" {
					if response != nil || calls.Load() != 0 || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted {
						t.Fatalf("unsupported style reached transport: response=%v calls=%d error=%v", response, calls.Load(), err)
					}
				} else if err != nil || response == nil || calls.Load() != 1 {
					t.Fatalf("supported style: response=%v calls=%d error=%v", response, calls.Load(), err)
				}
			})
		}
	}
}
