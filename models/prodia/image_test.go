package prodia_test

import (
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/prodia"
)

func TestImageModel_Call_Mock(t *testing.T) {
	// Prodia /job returns the raw image bytes directly (sync endpoint).
	srv := modeltest.MuxServer(modeltest.Route{Method: "POST", Contains: "/job", Handle: func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "image/png" {
			t.Errorf("Accept = %q", got)
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("FAKE-PNG-BYTES"))
	}})
	t.Cleanup(srv.Close)

	opts := image.Options{Model: prodia.JobFluxFastSchnellTextToImage}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	m, err := prodia.NewImageModel(t.Context(), prodia.ImageModelConfig{
		APIKey:         "test-key",
		DefaultOptions: opts,
		BaseURL:        srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	req, _ := image.NewRequest("a small red square")
	req.Options.OutputFormat = "image/png"
	out, err := m.Call(t.Context(), req)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out.First() == nil {
		t.Fatal("nil result")
	}
}
