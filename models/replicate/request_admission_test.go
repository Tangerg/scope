package replicate_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/models/replicate"
)

func TestImageOptionsRejectUnmappedOrNullControlsBeforeIO(t *testing.T) {
	for _, extension := range []string{`{"version":"unbound-version"}`, `null`} {
		t.Run(extension, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusBadRequest) }))
			t.Cleanup(server.Close)
			model, err := replicate.NewImageModel(t.Context(), replicate.ImageModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: image.Options{Model: replicate.ModelFluxSchnell}, InputSchema: replicate.FluxSchnellImageInputSchema()})
			if err != nil {
				t.Fatal(err)
			}
			request := &image.Request{Prompt: "landscape"}
			if setErr := request.Options.Extensions.Set(replicate.ImageRequestExtensionKey, json.RawMessage(extension)); setErr != nil {
				t.Fatal(setErr)
			}
			response, err := model.Call(t.Context(), request)
			if response != nil || err == nil || calls != 0 {
				t.Fatalf("response=%v error=%v calls=%d", response, err, calls)
			}
		})
	}
}
