//go:build integration

package openai_test

import (
	"testing"
	"time"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/protocol/openai"
)

func TestImageModel_Call_Integration(t *testing.T) {
	key := modeltest.RequireKey(t, "openai")
	modelID, _ := modeltest.LookupEnv("SCOPE_TEST_OPENAI_IMAGE_MODEL")
	if modelID == "" {
		modelID = "dall-e-2" // cheaper than dall-e-3
	}
	opts := image.Options{Model: modelID}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	m, err := openai.NewImageModel(t.Context(), openai.ImageModelConfig{
		Provider:       "openai",
		APIKey:         key,
		DefaultOptions: opts,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := modeltest.WithTimeout(t, 60*time.Second)
	defer cancel()
	req, _ := image.NewRequest("a small red square on white background")
	out, err := m.Call(ctx, req)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out.First() == nil {
		t.Fatal("nil result")
	}
}
