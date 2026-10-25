package openai_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/moderation"
	"github.com/Tangerg/scope/models/protocol/openai"
)

func TestEmbeddingRejectsAbsentResponse(t *testing.T) {
	for _, body := range []string{`null`, `{}`} {
		t.Run(body, func(t *testing.T) {
			server := modeltest.JSONServer(http.StatusOK, body)
			defer server.Close()
			model := newEmbeddingModel(t, server.URL, "embedding-model")
			request, err := embedding.NewRequest([]string{"text"})
			if err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(t.Context(), request)
			if response != nil || !errors.Is(err, embedding.ErrInvalidResponse) {
				t.Fatalf("response=%v error=%v", response, err)
			}
		})
	}
}

func TestImageRejectsAbsentResponse(t *testing.T) {
	for _, body := range []string{`null`, `{}`} {
		t.Run(body, func(t *testing.T) {
			server := modeltest.JSONServer(http.StatusOK, body)
			defer server.Close()
			model := newImageModel(t, server.URL, "image-model")
			request, err := image.NewRequest("an image")
			if err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(t.Context(), request)
			if response != nil || !errors.Is(err, image.ErrInvalidResponse) {
				t.Fatalf("response=%v error=%v", response, err)
			}
		})
	}
}

func TestModerationRejectsAbsentResponse(t *testing.T) {
	for _, body := range []string{`null`, `{}`} {
		t.Run(body, func(t *testing.T) {
			server := modeltest.JSONServer(http.StatusOK, body)
			defer server.Close()
			model, err := openai.NewModerationModel(t.Context(), openai.ModerationModelConfig{
				Provider: "openai", APIKey: "test", BaseURL: server.URL,
				DefaultOptions: moderation.Options{Model: "moderation-model"},
			})
			if err != nil {
				t.Fatal(err)
			}
			request, err := moderation.NewRequest([]string{"text"})
			if err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(t.Context(), request)
			if response != nil || !errors.Is(err, moderation.ErrInvalidResponse) {
				t.Fatalf("response=%v error=%v", response, err)
			}
		})
	}
}
