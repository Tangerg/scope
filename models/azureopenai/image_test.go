package azureopenai_test

import (
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/azureopenai"
)

const azureImageJSON = `{"created":1700000000,"data":[{"url":"https://cdn.test/img.png"}]}`

func TestImageModel_Call_Mock(t *testing.T) {
	srv := modeltest.JSONServer(http.StatusOK, azureImageJSON)
	t.Cleanup(srv.Close)

	opts := image.Options{Model: "dall-e-3-deployment"}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	m, err := azureopenai.NewImageModel(t.Context(), azureopenai.ImageModelConfig{
		Config:         azureopenai.Config{APIKey: "test-key", BaseURL: srv.URL + "/openai/v1/"},
		DefaultOptions: opts,
	})
	if err != nil {
		t.Fatal(err)
	}

	req, _ := image.NewRequest("a small red square")
	out, err := m.Call(t.Context(), req)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out.First() == nil {
		t.Fatal("nil result")
	}
}
