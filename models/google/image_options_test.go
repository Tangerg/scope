package google_test

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/models/google"
)

func TestImageExtensionOwnsStrictDecoding(t *testing.T) {
	for _, encoded := range []string{`null`, `{"seed":7}`, `{"google_search":{"unknown":true}}`, `{"safety_settings":[{"unknown":true}]}`} {
		var options google.ImageGenerationOptions
		if err := jsonv2.Unmarshal([]byte(encoded), &options); err == nil {
			t.Fatalf("the public extension accepted unsupported data: %s", encoded)
		}
	}
}

func TestImagePublicOptionsProjectOntoTheInteractionsRequest(t *testing.T) {
	wire := make(chan json.RawMessage, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1beta/interactions" || r.Header.Get("x-goog-api-key") != "test-key" {
			t.Errorf("wrong Interactions boundary: %s %s", r.Method, r.URL)
		}
		var body json.RawMessage
		if err := jsonv2.UnmarshalRead(r.Body, &body); err != nil {
			t.Error(err)
		}
		wire <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"result","status":"completed","steps":[{"type":"model_output","content":[{"type":"image","uri":"https://assets.example/result.jpg","mime_type":"image/jpeg"}]}]}`)
	}))
	defer server.Close()
	inline, err := media.NewBytes("image/png", []byte{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	linked, err := media.NewURI("image/jpeg", "https://assets.example/input.jpg")
	if err != nil {
		t.Fatal(err)
	}
	defaults := image.Options{Model: google.ModelGemini31FlashImage, OutputFormat: "image/jpeg", Seed: new(int64(0))}
	options := google.ImageGenerationOptions{
		AspectRatio: google.ImageAspectRatio16x9, ImageSize: google.ImageSize2K, Delivery: google.ImageDeliveryURI,
		PreviousInteractionID: "previous", Store: new(false), ThinkingLevel: google.ImageThinkingLevelHigh,
		ThinkingSummaries: google.ImageThinkingSummaryModeNone, ServiceTier: google.ImageServiceTierPriority,
		Labels: map[string]string{"purpose": "test"}, InputImages: []*media.Media{inline, linked},
		GoogleSearch:   &google.ImageGoogleSearchOptions{SearchTypes: []google.ImageSearchType{google.ImageSearchTypeWeb, google.ImageSearchTypeImage}},
		SafetySettings: []google.ImageSafetySetting{{Type: google.ImageSafetyCategoryHateSpeech, Threshold: google.ImageSafetyThresholdBlockMediumAndAbove, Method: google.ImageSafetyMethodProbability}},
	}
	if extensionErr := defaults.Extensions.Set(google.ImageRequestExtensionKey, options); extensionErr != nil {
		t.Fatal(extensionErr)
	}
	model, err := google.NewImageModel(t.Context(), google.ImageModelConfig{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client(), DefaultOptions: defaults})
	if err != nil {
		t.Fatal(err)
	}
	if extensionErr := defaults.Extensions.Set(google.ImageRequestExtensionKey, google.ImageGenerationOptions{ImageSize: google.ImageSize4K}); extensionErr != nil {
		t.Fatal(extensionErr)
	}
	result, err := model.Call(t.Context(), &image.Request{Prompt: "edit the images"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Outputs) != 1 || result.First().Media.Source.URI != "https://assets.example/result.jpg" {
		t.Fatalf("URI image result = %+v", result.Outputs)
	}
	want := jsontext.Value(`{
		"model":"gemini-3.1-flash-image",
		"input":[{"type":"text","text":"edit the images"},{"type":"image","data":"AQID","mime_type":"image/png"},{"type":"image","uri":"https://assets.example/input.jpg","mime_type":"image/jpeg"}],
		"tools":[{"type":"google_search","search_types":["web_search","image_search"]}],
		"response_format":{"type":"image","mime_type":"image/jpeg","aspect_ratio":"16:9","image_size":"2K","delivery":"uri"},
		"store":false,"generation_config":{"seed":0,"thinking_level":"high","thinking_summaries":"none"},
		"previous_interaction_id":"previous","labels":{"purpose":"test"},
		"safety_settings":[{"type":"hate_speech","threshold":"block_medium_and_above","method":"probability"}],"service_tier":"priority"
	}`)
	got := jsontext.Value(<-wire)
	if err := want.Canonicalize(); err != nil {
		t.Fatal(err)
	}
	if err := got.Canonicalize(); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("Interactions request = %s, want %s, error=%v", got, want, err)
	}
}

func TestImageOptionAdmissionPrecedesProviderIO(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	for _, test := range []struct{ name, model, options string }{
		{"aspect ratio", google.ModelGemini31FlashImage, `{"aspect_ratio":"arbitrary"}`},
		{"image size", google.ModelGemini31FlashImage, `{"image_size":"8K"}`},
		{"delivery", google.ModelGemini31FlashImage, `{"delivery":"download"}`},
		{"thinking level", google.ModelGemini31FlashImage, `{"thinking_level":"unknown"}`},
		{"thinking summary", google.ModelGemini31FlashImage, `{"thinking_summaries":"all"}`},
		{"service tier", google.ModelGemini31FlashImage, `{"service_tier":"premium"}`},
		{"search type", google.ModelGemini31FlashImage, `{"google_search":{"search_types":["unknown"]}}`},
		{"safety category", google.ModelGemini31FlashImage, `{"safety_settings":[{"type":"unknown","threshold":"off"}]}`},
		{"safety threshold", google.ModelGemini31FlashImage, `{"safety_settings":[{"type":"hate_speech","threshold":"unknown"}]}`},
		{"safety method", google.ModelGemini31FlashImage, `{"safety_settings":[{"type":"hate_speech","threshold":"off","method":"unknown"}]}`},
		{"lite size", google.ModelGemini31FlashLiteImage, `{"image_size":"2K"}`},
		{"lite search", google.ModelGemini31FlashLiteImage, `{"google_search":{}}`},
		{"lite thinking", google.ModelGemini31FlashLiteImage, `{"thinking_level":"low"}`},
		{"flash size", google.ModelGemini25FlashImage, `{"image_size":"2K"}`},
		{"pro size", google.ModelGemini3ProImage, `{"image_size":"512"}`},
		{"unknown input MIME", google.ModelGemini31FlashImage, `{"input_images":[{"source":{"kind":"uri","uri":"https://assets.example/image.png"}}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			model, err := google.NewImageModel(t.Context(), google.ImageModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: image.Options{Model: test.model}})
			if err != nil {
				t.Fatal(err)
			}
			request := &image.Request{Prompt: "edit an image"}
			if extensionErr := request.Options.Extensions.Set(google.ImageRequestExtensionKey, json.RawMessage(test.options)); extensionErr != nil {
				t.Fatal(extensionErr)
			}
			result, err := model.Call(t.Context(), request)
			failure, found := errors.AsType[*invocation.Failure](err)
			if result != nil || !found || failure.Kind != invocation.FailureNotExecuted || calls.Load() != 0 {
				t.Fatalf("invalid option reached I/O: result=%v error=%v calls=%d", result, err, calls.Load())
			}
		})
	}
}
