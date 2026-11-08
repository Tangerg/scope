package luma_test

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/models/luma"
)

func TestImageNativeOptionsThroughPublicCall(t *testing.T) {
	for _, extension := range []any{
		map[string]any{"aspect_ratio": "16:9", "type": "image_edit", "source": map[string]any{"url": "https://example.test/source.png"}, "image_ref": []any{map[string]any{"file_id": "file-1"}}, "web_search": false, "style": "manga", "user_id": "user-1"},
		luma.ImageRequestOptions{AspectRatio: "16:9", Type: "image_edit", Source: &luma.ImageReference{URL: "https://example.test/source.png"}, ImageRef: []luma.ImageReference{{FileID: "file-1"}}, WebSearch: new(false), Style: "manga", UserID: "user-1"},
	} {
		t.Run(fmt.Sprintf("%T", extension), func(t *testing.T) {
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodPost {
					var body map[string]any
					if err := jsonv2.UnmarshalRead(r.Body, &body); err != nil {
						t.Error(err)
					}
					if body["aspect_ratio"] != "16:9" || body["type"] != "image_edit" || body["prompt"] != "landscape" || body["model"] != "uni-1" || body["output_format"] != "png" || body["web_search"] != false || body["style"] != "manga" || body["user_id"] != "user-1" {
						t.Errorf("generation body=%v", body)
					}
					if source, ok := body["source"].(map[string]any); !ok || source["url"] != "https://example.test/source.png" {
						t.Errorf("source=%v", body["source"])
					}
					if refs, ok := body["image_ref"].([]any); !ok || len(refs) != 1 {
						t.Errorf("references=%v", body["image_ref"])
					}
					fmt.Fprint(w, `{"id":"gen-1","state":"queued"}`)
				} else if r.URL.Path == "/output.png" {
					w.Header().Set("Content-Type", "image/png")
					fmt.Fprint(w, "PNG")
				} else {
					fmt.Fprintf(w, `{"id":"gen-1","created_at":"2026-09-27T00:00:00Z","state":"completed","output":[{"type":"image","url":%q}]}`, server.URL+"/output.png")
				}
			}))
			defer server.Close()
			opts := image.Options{Model: luma.ModelUni1, OutputFormat: "image/png"}
			if err := opts.Extensions.Set(luma.ImageRequestExtensionKey, extension); err != nil {
				t.Fatal(err)
			}
			model, err := luma.NewImageModel(t.Context(), luma.ImageModelConfig{APIKey: "test", BaseURL: server.URL, OutputOrigins: []string{server.URL}, DefaultOptions: opts})
			if err != nil {
				t.Fatal(err)
			}
			result, err := model.Call(t.Context(), &image.Request{Prompt: "landscape"})
			if err != nil {
				t.Fatal(err)
			}
			if string(result.First().Media.Source.Bytes) != "PNG" {
				t.Fatalf("output=%v", result)
			}
		})
	}
}

func TestImageRejectsIllegalNativeReferencesBeforeIO(t *testing.T) {
	url := luma.ImageReference{URL: "https://example.test/image.png"}
	for _, test := range []struct {
		name    string
		options luma.ImageRequestOptions
	}{
		{"edit without source", luma.ImageRequestOptions{Type: "image_edit"}},
		{"generation with source", luma.ImageRequestOptions{Source: &url}},
		{"empty reference", luma.ImageRequestOptions{ImageRef: []luma.ImageReference{{}}}},
		{"multiple identities", luma.ImageRequestOptions{ImageRef: []luma.ImageReference{{URL: url.URL, FileID: "file-1"}}}},
		{"inline without media type", luma.ImageRequestOptions{ImageRef: []luma.ImageReference{{Data: "UE5H"}}}},
		{"inline without image media", luma.ImageRequestOptions{ImageRef: []luma.ImageReference{{Data: "UE5H", MediaType: "text/plain"}}}},
		{"invalid base64", luma.ImageRequestOptions{ImageRef: []luma.ImageReference{{Data: "not base64!", MediaType: "image/png"}}}},
		{"media without inline data", luma.ImageRequestOptions{ImageRef: []luma.ImageReference{{URL: url.URL, MediaType: "image/png"}}}},
		{"generation count", luma.ImageRequestOptions{ImageRef: []luma.ImageReference{url, url, url, url, url, url, url, url, url, url}}},
		{"edit count", luma.ImageRequestOptions{Type: "image_edit", Source: &url, ImageRef: []luma.ImageReference{url, url, url, url, url, url, url, url, url}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusBadRequest) }))
			defer server.Close()
			model, err := luma.NewImageModel(t.Context(), luma.ImageModelConfig{APIKey: "test", BaseURL: server.URL, OutputOrigins: []string{server.URL}, DefaultOptions: image.Options{Model: luma.ModelUni1}})
			if err != nil {
				t.Fatal(err)
			}
			request := &image.Request{Prompt: "landscape"}
			if err := request.Options.Extensions.Set(luma.ImageRequestExtensionKey, test.options); err != nil {
				t.Fatal(err)
			}
			if _, err := model.Call(t.Context(), request); invocation.ClassifyFailure(err) != invocation.FailureNotExecuted {
				t.Fatalf("error=%v", err)
			}
			if calls.Load() != 0 {
				t.Fatalf("calls=%d", calls.Load())
			}
		})
	}
}

func TestImageNativeReferenceAdmissionAcceptsDocumentedBoundaries(t *testing.T) {
	reference := luma.ImageReference{URL: "https://example.test/image.png"}
	for _, options := range []luma.ImageRequestOptions{
		{ImageRef: []luma.ImageReference{reference, reference, reference, reference, reference, reference, reference, reference, reference}},
		{Type: "image_edit", Source: &reference, ImageRef: []luma.ImageReference{reference, reference, reference, reference, reference, reference, reference, reference}},
		{ImageRef: []luma.ImageReference{{Data: "UE5H", MediaType: "image/png"}}},
	} {
		if err := options.Validate(); err != nil {
			t.Fatalf("valid reference boundary rejected: %v", err)
		}
	}
}

func TestImageRejectsNativeCoreFieldsBeforeIO(t *testing.T) {
	for _, field := range []string{"prompt", "model", "output_format"} {
		t.Run(field, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusBadRequest) }))
			defer server.Close()
			opts := image.Options{Model: luma.ModelUni1}
			if err := opts.Extensions.Set(luma.ImageRequestExtensionKey, map[string]any{field: nil}); err != nil {
				t.Fatal(err)
			}
			model, err := luma.NewImageModel(t.Context(), luma.ImageModelConfig{APIKey: "test", BaseURL: server.URL, OutputOrigins: []string{server.URL}, DefaultOptions: opts})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := model.Call(t.Context(), &image.Request{Prompt: "landscape"}); err == nil || calls != 0 {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
		})
	}
}
