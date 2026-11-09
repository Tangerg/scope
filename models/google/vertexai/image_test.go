package vertexai_test

import (
	"encoding/base64"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/invocation"
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

func TestImagePeopleControlsAreAdmittedBeforeTransport(t *testing.T) {
	for _, test := range []struct {
		name      string
		options   vertexai.ImageGenerationOptions
		person    string
		prominent string
		accepted  bool
	}{
		{name: "omitted", accepted: true},
		{name: "all people", options: vertexai.ImageGenerationOptions{PersonGeneration: "ALLOW_ALL"}, person: "ALLOW_ALL", accepted: true},
		{name: "adults", options: vertexai.ImageGenerationOptions{PersonGeneration: "ALLOW_ADULT"}, person: "ALLOW_ADULT", accepted: true},
		{name: "no people", options: vertexai.ImageGenerationOptions{PersonGeneration: "ALLOW_NONE"}, person: "ALLOW_NONE", accepted: true},
		{name: "unspecified people", options: vertexai.ImageGenerationOptions{PersonGeneration: "PERSON_GENERATION_UNSPECIFIED"}, person: "PERSON_GENERATION_UNSPECIFIED", accepted: true},
		{name: "allow prominent", options: vertexai.ImageGenerationOptions{ProminentPeople: "ALLOW_PROMINENT_PEOPLE"}, prominent: "ALLOW_PROMINENT_PEOPLE", accepted: true},
		{name: "block prominent", options: vertexai.ImageGenerationOptions{ProminentPeople: "BLOCK_PROMINENT_PEOPLE"}, prominent: "BLOCK_PROMINENT_PEOPLE", accepted: true},
		{name: "unspecified prominent", options: vertexai.ImageGenerationOptions{ProminentPeople: "PROMINENT_PEOPLE_UNSPECIFIED"}, prominent: "PROMINENT_PEOPLE_UNSPECIFIED", accepted: true},
		{name: "unknown people", options: vertexai.ImageGenerationOptions{PersonGeneration: "unknown"}},
		{name: "imagen vocabulary", options: vertexai.ImageGenerationOptions{PersonGeneration: "DONT_ALLOW"}},
		{name: "unknown prominent", options: vertexai.ImageGenerationOptions{ProminentPeople: "unknown"}},
		{name: "ratio beyond SDK comment", options: vertexai.ImageGenerationOptions{AspectRatio: "4:5"}, accepted: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body struct {
					GenerationConfig struct {
						ImageConfig struct {
							PersonGeneration string `json:"personGeneration"`
							ProminentPeople  string `json:"prominentPeople"`
							AspectRatio      string `json:"aspectRatio"`
						} `json:"imageConfig"`
					} `json:"generationConfig"`
				}
				if err := jsonv2.UnmarshalRead(r.Body, &body); err != nil {
					t.Error(err)
				}
				controls := body.GenerationConfig.ImageConfig
				if controls.PersonGeneration != test.person || controls.ProminentPeople != test.prominent || controls.AspectRatio != test.options.AspectRatio {
					t.Errorf("image controls = %+v", controls)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"UE5H"}}]}}]}`)
			}))
			defer server.Close()
			model, err := vertexai.NewImageModel(t.Context(), vertexai.ImageModelConfig{Client: vertexai.ClientConfig{Project: "project", Location: vertexai.LocationGlobal, BaseURL: server.URL, HTTPClient: server.Client()}, DefaultOptions: image.Options{Model: vertexai.ModelGemini25FlashImage}})
			if err != nil {
				t.Fatal(err)
			}
			request, err := image.NewRequest("cube")
			if err != nil {
				t.Fatal(err)
			}
			if setErr := request.Options.Extensions.Set(vertexai.ImageRequestExtensionKey, test.options); setErr != nil {
				t.Fatal(setErr)
			}
			response, err := model.Call(t.Context(), request)
			if test.accepted {
				if err != nil || response == nil || calls.Load() != 1 {
					t.Fatalf("supported controls: response=%v calls=%d error=%v", response, calls.Load(), err)
				}
			} else if response != nil || calls.Load() != 0 || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted {
				t.Fatalf("unsupported controls reached transport: response=%v calls=%d error=%v", response, calls.Load(), err)
			}
		})
	}
}
