package stability_test

import (
	"encoding/base64"
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/stability"
)

func TestImageModel_Call_Mock(t *testing.T) {
	imgB64 := base64.StdEncoding.EncodeToString([]byte("FAKE-PNG-BYTES"))
	body := `{"image":"` + imgB64 + `","finish_reason":"SUCCESS","seed":42}`
	srv := modeltest.JSONServer(http.StatusOK, body)
	t.Cleanup(srv.Close)

	opts := image.Options{Model: stability.ModelCore}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	m, err := stability.NewImageModel(t.Context(), stability.ImageModelConfig{
		APIKey:         "test-key",
		DefaultOptions: opts,
		BaseURL:        srv.URL,
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
