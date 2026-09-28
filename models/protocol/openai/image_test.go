package openai_test

import (
	jsonv2 "encoding/json/v2"
	"net/http"
	"strings"
	"testing"

	openaisdk "github.com/openai/openai-go/v3"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/protocol/openai"
)

func newImageModel(t *testing.T, baseURL, modelID string) *openai.ImageModel {
	t.Helper()
	opts := image.Options{Model: modelID}
	err := opts.Validate()
	if err != nil {
		t.Fatalf("NewOptions: %v", err)
	}
	m, err := openai.NewImageModel(t.Context(), openai.ImageModelConfig{
		Provider:       "openai",
		APIKey:         "test-key",
		DefaultOptions: opts,
		BaseURL:        baseURL,
	})
	if err != nil {
		t.Fatalf("NewImageModel: %v", err)
	}
	return m
}

func TestImageModel_Call_Mock(t *testing.T) {
	resp := openaisdk.ImagesResponse{
		Created: 1700000000,
		Data: []openaisdk.Image{
			{URL: "https://example.com/img1.png", B64JSON: ""},
			{URL: "https://example.com/img2.png", B64JSON: ""},
		},
	}
	body, _ := jsonv2.Marshal(resp)

	var seenURL string
	srv := modeltest.JSONServer(http.StatusOK, string(body), func(r *http.Request) {
		seenURL = r.URL.Path
	})
	t.Cleanup(srv.Close)

	m := newImageModel(t, srv.URL, "test-image-model")
	req, err := image.NewRequest("a serene mountain lake at sunset")
	if err != nil {
		t.Fatal(err)
	}

	out, err := m.Call(t.Context(), req)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !strings.HasSuffix(seenURL, "/images/generations") {
		t.Errorf("URL = %q; want /images/generations suffix", seenURL)
	}
	if len(out.Outputs) != 2 || out.First() == nil {
		t.Fatalf("outputs = %#v", out.Outputs)
	}
	if got, err := out.Outputs[1].Media.URI(); err != nil || got != "https://example.com/img2.png" {
		t.Fatalf("second URI = %q, %v", got, err)
	}
}

func TestImageSizeIsOmittedUntilRequested(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		server := modeltest.JSONServer(http.StatusOK, `{"data":[{"b64_json":"aW1hZ2U="}]}`, func(request *http.Request) {
			var body map[string]any
			if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
				t.Error(err)
			}
			size, found := body["size"]
			if explicit {
				if size != "1024x1024" {
					t.Errorf("size = %#v", size)
				}
			} else if found {
				t.Errorf("unspecified size = %#v", size)
			}
		})
		model := newImageModel(t, server.URL, "test-image-model")
		request, err := image.NewRequest("an image")
		if err != nil {
			t.Fatal(err)
		}
		if explicit {
			value := int64(1024)
			request.Options.Width = &value
			request.Options.Height = &value
		}
		_, err = model.Call(t.Context(), request)
		server.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
}
