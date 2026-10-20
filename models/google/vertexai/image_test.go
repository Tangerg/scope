package vertexai_test

import (
	"encoding/base64"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/models/google/vertexai"
)

func TestImageModelUsesVertexGenerateContent(t *testing.T) {
	imageData := base64.StdEncoding.EncodeToString([]byte("PNG"))
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.Contains(request.URL.Path, ":generateContent") {
			t.Errorf("path = %q", request.URL.Path)
		}
		var body map[string]any
		if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		generationConfig, _ := body["generationConfig"].(map[string]any)
		modalities, _ := generationConfig["responseModalities"].([]any)
		if len(modalities) != 1 || modalities[0] != "IMAGE" {
			t.Errorf("responseModalities = %#v", modalities)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"responseId":"response-1",
			"modelVersion":"gemini-2.5-flash-image-001",
			"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[
				{"thought":true,"inlineData":{"mimeType":"image/png","data":"` + imageData + `"}},
				{"text":"done"},
				{"inlineData":{"mimeType":"image/png","data":"` + imageData + `"}}
			]}}]
		}`))
	}))
	t.Cleanup(server.Close)

	defaultOptions := image.Options{Model: vertexai.ModelGemini25FlashImage}
	err := defaultOptions.Validate()
	if err != nil {
		t.Fatal(err)
	}
	model, err := vertexai.NewImageModel(t.Context(), vertexai.ImageModelConfig{
		Client: vertexai.ClientConfig{
			Project: "test-project", Location: vertexai.LocationGlobal,
			BaseURL: server.URL, HTTPClient: server.Client(),
		},
		DefaultOptions: defaultOptions,
	})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := image.NewRequest("a scope in snow")
	response, err := model.Call(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Outputs) != 1 || string(response.Outputs[0].Media.Source.Bytes) != "PNG" {
		t.Fatalf("outputs = %#v", response.Outputs)
	}
}

func TestImageRequiresStopForEveryCandidate(t *testing.T) {
	for _, reason := range []string{"STOP", "MAX_TOKENS", "SAFETY", "OTHER", "unknown", ""} {
		t.Run(reason, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"candidates":[{"finishReason":%q,"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"UE5H"}}]}}]}`, reason)
			}))
			defer server.Close()
			model, err := vertexai.NewImageModel(t.Context(), vertexai.ImageModelConfig{Client: vertexai.ClientConfig{Project: "project", Location: vertexai.LocationGlobal, BaseURL: server.URL, HTTPClient: server.Client()}, DefaultOptions: image.Options{Model: vertexai.ModelGemini25FlashImage}})
			if err != nil {
				t.Fatal(err)
			}
			request, _ := image.NewRequest("cube")
			response, err := model.Call(t.Context(), request)
			if (reason == "STOP") != (err == nil) || (err != nil && response != nil) {
				t.Fatalf("finish %q produced %v, %v", reason, response, err)
			}
		})
	}
}
