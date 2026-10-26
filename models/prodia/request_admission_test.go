package prodia_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/models/prodia"
)

func TestImageOptionsRejectUnmappedOrNullControlsBeforeIO(t *testing.T) {
	for _, extension := range []string{`{"steps":10}`, `null`} {
		t.Run(extension, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusBadRequest) }))
			t.Cleanup(server.Close)
			model, err := prodia.NewImageModel(t.Context(), prodia.ImageModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: image.Options{Model: prodia.JobFlux2DevTextToImage}})
			if err != nil {
				t.Fatal(err)
			}
			request := &image.Request{Prompt: "landscape"}
			if setErr := request.Options.Extensions.Set(prodia.ImageRequestExtensionKey, json.RawMessage(extension)); setErr != nil {
				t.Fatal(setErr)
			}
			response, err := model.Call(t.Context(), request)
			if response != nil || err == nil || calls != 0 {
				t.Fatalf("response=%v error=%v calls=%d", response, err, calls)
			}
		})
	}
}
