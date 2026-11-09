package blackforestlabs_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/blackforestlabs"
)

func TestPromptUpsamplingPresenceSurvivesProjection(t *testing.T) {
	for _, modelName := range []string{"flux-2-flex", blackforestlabs.ModelFluxDev} {
		for _, defaults := range []bool{false, true} {
			for _, value := range []string{"", "false", "true"} {
				t.Run(fmt.Sprintf("%s/defaults=%t/value=%s", modelName, defaults, value), func(t *testing.T) {
					var endpoint string
					server := modeltest.MuxServer(
						modeltest.Route{Method: http.MethodPost, Contains: modelName, Handle: func(writer http.ResponseWriter, request *http.Request) {
							var body map[string]json.RawMessage
							if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
								t.Error(err)
							}
							if got := string(body["prompt_upsampling"]); got != value {
								t.Errorf("prompt_upsampling = %q, want %q", got, value)
							}
							writer.Header().Set("Content-Type", "application/json")
							_, _ = fmt.Fprintf(writer, `{"id":"task","polling_url":%q}`, endpoint+"/get_result")
						}},
						modeltest.Route{Method: http.MethodGet, Contains: "get_result", Handle: func(writer http.ResponseWriter, _ *http.Request) {
							writer.Header().Set("Content-Type", "application/json")
							_, _ = fmt.Fprintf(writer, `{"id":"task","status":"Ready","result":{"sample":%q}}`, endpoint+"/output.png")
						}},
						modeltest.Route{Method: http.MethodGet, Contains: "output.png", Handle: func(writer http.ResponseWriter, _ *http.Request) {
							writer.Header().Set("Content-Type", "image/png")
							_, _ = writer.Write([]byte("PNG"))
						}},
					)
					defer server.Close()
					endpoint = server.URL
					options := image.Options{Model: modelName}
					request := &image.Request{Prompt: "cube"}
					if value != "" {
						target := &request.Options
						if defaults {
							target = &options
						}
						if err := target.Extensions.Set(blackforestlabs.ImageRequestExtensionKey, json.RawMessage(`{"prompt_upsampling":`+value+`}`)); err != nil {
							t.Fatal(err)
						}
					}
					model, err := blackforestlabs.NewImageModel(t.Context(), blackforestlabs.ImageModelConfig{
						APIKey: "test", BaseURL: endpoint, OutputOrigins: []string{endpoint}, DefaultOptions: options, PollInterval: time.Millisecond,
					})
					if err != nil {
						t.Fatal(err)
					}
					if _, err := model.Call(t.Context(), request); err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}
