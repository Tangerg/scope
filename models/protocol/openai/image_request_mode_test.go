package openai_test

import (
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/protocol/openai"
)

func TestImageRequestOwnsUnaryResponseMode(t *testing.T) {
	for _, field := range []string{"stream", "partial_images"} {
		for _, test := range []struct {
			name  string
			value any
		}{{"true", true}, {"false", false}, {"null", nil}, {"zero", 0}, {"string", "true"}} {
			t.Run(field+"/"+test.name, func(t *testing.T) {
				var requests modeltest.PollCounter
				server := modeltest.JSONServer(http.StatusOK, `{"data":[{"b64_json":"aW1hZ2U="}]}`, func(*http.Request) { requests.Inc() })
				t.Cleanup(server.Close)
				model := newImageModel(t, server.URL, "gpt-image-1")
				request, err := image.NewRequest("a square")
				if err != nil {
					t.Fatal(err)
				}
				if setErr := request.Options.Extensions.Set(openai.ImageRequestExtensionKey, openai.RequestFields{field: test.value}); setErr != nil {
					t.Fatal(setErr)
				}
				response, err := model.Call(t.Context(), request)
				if err == nil || response != nil || requests.N() != 0 {
					t.Fatalf("response = %v; error = %v; requests = %d", response, err, requests.N())
				}
			})
		}
	}
}
