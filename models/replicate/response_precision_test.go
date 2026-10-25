package replicate_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/replicate"
)

func TestPredictionResponsePreservesInputJSONNumbers(t *testing.T) {
	const exact = `{"seed":9007199254740993,"nested":{"id":18446744073709551615}}`
	for _, polled := range []bool{false, true} {
		for _, kind := range []string{"image", "speech"} {
			t.Run(fmt.Sprintf("%s/polled=%t", kind, polled), func(t *testing.T) {
				var server *httptest.Server
				server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/output" {
						w.Header().Set("Content-Type", map[string]string{"image": "image/png", "speech": "audio/mpeg"}[kind])
						_, _ = w.Write([]byte("MEDIA"))
						return
					}
					w.Header().Set("Content-Type", "application/json")
					if polled && r.Method == http.MethodPost {
						fmt.Fprint(w, `{"id":"prediction","status":"starting"}`)
						return
					}
					fmt.Fprintf(w, `{"id":"prediction","status":"succeeded","input":%s,"output":%q}`, exact, server.URL+"/output")
				}))
				defer server.Close()
				var extra metadata.Map
				var key string
				if kind == "image" {
					model, err := replicate.NewImageModel(t.Context(), replicate.ImageModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: image.Options{Model: "owner/model"}, InputSchema: replicate.ImageInputSchema{PromptKey: "prompt", OutputKind: replicate.FileOutputURI}, PollInterval: time.Millisecond})
					if err != nil {
						t.Fatal(err)
					}
					request, err := image.NewRequest("image")
					if err != nil {
						t.Fatal(err)
					}
					response, err := model.Call(t.Context(), request)
					if err != nil {
						t.Fatal(err)
					}
					extra = response.Metadata.Extra
					key = replicate.ImageResponseExtensionKey
				} else {
					model, err := replicate.NewSpeechModel(t.Context(), replicate.SpeechModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: speech.Options{Model: "owner/model"}, InputSchema: replicate.SpeechInputSchema{TextKey: "text", OutputKind: replicate.FileOutputURI}, PollInterval: time.Millisecond})
					if err != nil {
						t.Fatal(err)
					}
					request, err := speech.NewRequest("hello")
					if err != nil {
						t.Fatal(err)
					}
					response, err := model.Call(t.Context(), request)
					if err != nil {
						t.Fatal(err)
					}
					extra = response.Metadata.Extra
					key = replicate.SpeechResponseExtensionKey
				}
				var response struct {
					Input json.RawMessage `json:"input"`
				}
				if err := jsonv2.Unmarshal(extra[key], &response); err != nil {
					t.Fatal(err)
				}
				var input struct {
					Seed   uint64 `json:"seed"`
					Nested struct {
						ID uint64 `json:"id"`
					} `json:"nested"`
				}
				if err := jsonv2.Unmarshal(response.Input, &input); err != nil {
					t.Fatal(err)
				}
				if input.Seed != 9007199254740993 || input.Nested.ID != 18446744073709551615 {
					t.Fatalf("response input=%s", response.Input)
				}
			})
		}
	}
}
