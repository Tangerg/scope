package blackforestlabs_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/models/blackforestlabs"
)

func TestGenerationModelOwnsOnlyOnePathSegment(t *testing.T) {
	for _, test := range []struct{ model, segment string }{
		{"flux-pro-1.1", "flux-pro-1.1"}, {"flux-custom_1.0", "flux-custom_1.0"},
		{"flux-pro-1.1?alternate=true#", "flux-pro-1.1%3Falternate=true%23"}, {"flux#fragment", "flux%23fragment"},
		{"flux/segment", "flux%2Fsegment"}, {`flux\segment`, "flux%5Csegment"}, {"flux%2fsegment", "flux%252fsegment"},
		{".", "%2E"}, {"..", "%2E%2E"}, {"%2e%2e", "%252e%252e"},
	} {
		for _, defaults := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/defaults=%t", test.model, defaults), func(t *testing.T) {
				var calls atomic.Int32
				var endpoint string
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					calls.Add(1)
					writer.Header().Set("Content-Type", "application/json")
					switch request.Method {
					case http.MethodPost:
						if request.URL.EscapedPath() != "/v1/"+test.segment || request.URL.RawQuery != "" {
							t.Errorf("generation URI=%q", request.RequestURI)
						}
						_, _ = fmt.Fprintf(writer, `{"id":"task-1","polling_url":%q}`, endpoint+"/result")
					default:
						if request.URL.Path == "/result" {
							_, _ = fmt.Fprintf(writer, `{"id":"task-1","status":"Ready","result":{"sample":%q}}`, endpoint+"/image")
							return
						}
						writer.Header().Set("Content-Type", "image/png")
						_, _ = writer.Write([]byte("PNG"))
					}
				}))
				defer server.Close()
				endpoint = server.URL
				options := image.Options{Model: blackforestlabs.ModelFluxDev}
				request, err := image.NewRequest("a cube")
				if err != nil {
					t.Fatal(err)
				}
				if defaults {
					options.Model = test.model
				} else {
					request.Options.Model = test.model
				}
				model, err := blackforestlabs.NewImageModel(t.Context(), blackforestlabs.ImageModelConfig{APIKey: "test", BaseURL: server.URL + "/v1", OutputOrigins: []string{server.URL}, DefaultOptions: options})
				if err != nil {
					t.Fatal(err)
				}
				response, err := model.Call(t.Context(), request)
				if err != nil || response.First() == nil || calls.Load() != 3 {
					t.Fatalf("response=%v error=%v requests=%d", response, err, calls.Load())
				}
			})
		}
	}
}
