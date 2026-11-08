package google_test

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"io"
	"net/http"
	"strings"
	"testing"

	"google.golang.org/genai"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/google"
	"github.com/Tangerg/scope/models/google/vertexai"
)

type generationCaptureTransport struct{ bodies [][]byte }

func (g *generationCaptureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	data, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	g.bodies = append(g.bodies, data)
	return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":400,"message":"audit capture boundary","status":"INVALID_ARGUMENT"}}`)), Request: req}, nil
}

func generationNativeConfig(t *testing.T, raw string) *genai.GenerateContentConfig {
	t.Helper()
	var config genai.GenerateContentConfig
	if err := jsonv2.Unmarshal([]byte(raw), &config, jsonv2.RejectUnknownMembers(true)); err != nil {
		t.Fatal(err)
	}
	return &config
}

func invokeGenerationSDK(t *testing.T, backend genai.Backend, raw string) ([][]byte, error) {
	t.Helper()
	transport := &generationCaptureTransport{}
	config := &genai.ClientConfig{Backend: backend, HTTPClient: &http.Client{Transport: transport}, HTTPOptions: genai.HTTPOptions{BaseURL: "https://audit.example.test"}}
	if backend == genai.BackendVertexAI {
		config.Project, config.Location = "test", "global"
	} else {
		config.APIKey = "audit"
	}
	client, err := genai.NewClient(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Models.GenerateContent(t.Context(), google.ModelGemini36Flash, []*genai.Content{genai.NewContentFromText("audit", genai.RoleUser)}, generationNativeConfig(t, raw))
	return transport.bodies, err
}

func invokeGenerationModel(t *testing.T, backend genai.Backend, modality, raw string) ([][]byte, error) {
	t.Helper()
	transport := &generationCaptureTransport{}
	httpClient := &http.Client{Transport: transport}
	vertexClient := vertexai.ClientConfig{Project: "test", Location: "global", BaseURL: "https://audit.example.test", HTTPClient: httpClient}
	provider := google.Provider
	if backend == genai.BackendVertexAI {
		provider = vertexai.Provider
	}
	var extensions metadata.Extensions
	suffix := "/request"
	if modality == "speech" || modality == "streaming_speech" {
		suffix = "/speech_request"
	}
	if modality == "transcription" {
		suffix = "/transcription_request"
	}
	if err := extensions.Set(provider+suffix, json.RawMessage(raw)); err != nil {
		t.Fatal(err)
	}
	var err error
	switch modality {
	case "chat":
		var model interface {
			Call(context.Context, *chat.Request) (*chat.Response, error)
		}
		if backend == genai.BackendVertexAI {
			model, err = vertexai.NewChat(t.Context(), vertexai.ChatConfig{Client: vertexClient, DefaultOptions: chat.Options{Model: google.ModelGemini36Flash}})
		} else {
			model, err = google.NewChat(t.Context(), google.ChatConfig{APIKey: "audit", BaseURL: "https://audit.example.test", HTTPClient: httpClient, DefaultOptions: chat.Options{Model: google.ModelGemini36Flash}})
		}
		if err != nil {
			t.Fatal(err)
		}
		_, err = model.Call(t.Context(), &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("audit"))}, Options: chat.Options{Extensions: extensions}})
	case "speech", "streaming_speech":
		modelName := google.ModelGemini25FlashPreviewTTS
		if modality == "streaming_speech" {
			modelName = google.ModelGemini31FlashTTSPreview
		}
		var model interface {
			Call(context.Context, *speech.Request) (*speech.Response, error)
		}
		if backend == genai.BackendVertexAI {
			config := vertexai.SpeechModelConfig{Client: vertexClient, DefaultOptions: speech.Options{Model: modelName}}
			if modality == "speech" {
				model, err = vertexai.NewSpeechModel(t.Context(), config)
			} else {
				model, err = vertexai.NewStreamingSpeechModel(t.Context(), config)
			}
		} else {
			config := google.SpeechModelConfig{APIKey: "audit", BaseURL: "https://audit.example.test", HTTPClient: httpClient, DefaultOptions: speech.Options{Model: modelName}}
			if modality == "speech" {
				model, err = google.NewSpeechModel(t.Context(), config)
			} else {
				model, err = google.NewStreamingSpeechModel(t.Context(), config)
			}
		}
		if err != nil {
			t.Fatal(err)
		}
		_, err = model.Call(t.Context(), &speech.Request{Text: "audit", Options: speech.Options{Extensions: extensions}})
	case "transcription":
		var model interface {
			Call(context.Context, *transcription.Request) (*transcription.Response, error)
		}
		if backend == genai.BackendVertexAI {
			model, err = vertexai.NewTranscriptionModel(t.Context(), vertexai.TranscriptionModelConfig{Client: vertexClient, DefaultOptions: transcription.Options{Model: google.ModelGemini36Flash}})
		} else {
			model, err = google.NewTranscriptionModel(t.Context(), google.TranscriptionModelConfig{APIKey: "audit", BaseURL: "https://audit.example.test", HTTPClient: httpClient, DefaultOptions: transcription.Options{Model: google.ModelGemini36Flash}})
		}
		if err != nil {
			t.Fatal(err)
		}
		var audio *media.Media
		audio, err = media.NewBytes("audio/wav", []byte("audit"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = model.Call(t.Context(), &transcription.Request{Audio: audio, Options: transcription.Options{Extensions: extensions}})
	default:
		t.Fatalf("unknown modality %s", modality)
	}
	return transport.bodies, err
}

func TestVertexImageControlsMatchCompleteSDKProjection(t *testing.T) {
	transport := &generationCaptureTransport{}
	model, err := vertexai.NewImageModel(t.Context(), vertexai.ImageModelConfig{Client: vertexai.ClientConfig{Project: "test", Location: "global", BaseURL: "https://audit.example.test", HTTPClient: &http.Client{Transport: transport}}, DefaultOptions: image.Options{Model: vertexai.ModelGemini3ProImage}})
	if err != nil {
		t.Fatal(err)
	}
	request := &image.Request{Prompt: "audit", Options: image.Options{OutputFormat: "image/jpeg"}}
	if extensionErr := request.Options.Extensions.Set(vertexai.ImageRequestExtensionKey, vertexai.ImageGenerationOptions{AspectRatio: "16:9", ImageSize: "2K", PersonGeneration: "ALLOW_ADULT", ProminentPeople: "BLOCK_PROMINENT_PEOPLE", OutputCompressionQuality: new(int32(75))}); extensionErr != nil {
		t.Fatal(extensionErr)
	}
	_, err = model.Call(t.Context(), request)
	if err == nil || len(transport.bodies) != 1 {
		t.Fatalf("bodies=%q error=%v", transport.bodies, err)
	}
	fullSDK, _ := invokeGenerationSDK(t, genai.BackendVertexAI, `{"imageConfig":{"aspectRatio":"16:9","imageSize":"2K","personGeneration":"ALLOW_ADULT","prominentPeople":"BLOCK_PROMINENT_PEOPLE","outputMimeType":"image/jpeg","outputCompressionQuality":75}}`)
	if len(fullSDK) != 1 {
		t.Fatalf("SDK calls=%d", len(fullSDK))
	}
	var want, got struct {
		Config struct {
			Image json.RawMessage `json:"imageConfig"`
		} `json:"generationConfig"`
	}
	if err := jsonv2.Unmarshal(fullSDK[0], &want); err != nil {
		t.Fatal(err)
	}
	if err := jsonv2.Unmarshal(transport.bodies[0], &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Config.Image) == 0 || !bytes.Equal(got.Config.Image, want.Config.Image) {
		t.Fatalf("image config=%s, SDK=%s", got.Config.Image, want.Config.Image)
	}
}

func TestGenerationRetainsCompleteSDKProjection(t *testing.T) {
	for _, test := range []struct {
		name      string
		backend   genai.Backend
		raw, want string
	}{
		{"gemini-image", genai.BackendGeminiAPI, `{"imageConfig":{"aspectRatio":"16:9","imageSize":"2K"}}`, `"imageConfig":{"aspectRatio":"16:9","imageSize":"2K"}`},
		{"vertex-image", genai.BackendVertexAI, `{"imageConfig":{"aspectRatio":"16:9","imageSize":"2K"}}`, `"imageConfig":{"aspectRatio":"16:9","imageSize":"2K"}`},
		{"vertex-model-selection", genai.BackendVertexAI, `{"modelSelectionConfig":{"featureSelectionPreference":"PRIORITIZE_QUALITY"}}`, `"modelConfig":{"featureSelectionPreference":"PRIORITIZE_QUALITY"}`},
		{"vertex-cached-content", genai.BackendVertexAI, `{"cachedContent":"locations/europe-west4/cachedContents/audit"}`, `"cachedContent":"projects/test/locations/europe-west4/cachedContents/audit"`},
		{"gemini-store", genai.BackendGeminiAPI, `{"store":false}`, `"store":false`},
		{"vertex-output-config", genai.BackendVertexAI, `{"generationOutputConfig":{"outputUriPrefix":"gs://audit/prefix/"}}`, `"outputConfig":{"outputUriPrefix":"gs://audit/prefix/"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, modality := range []string{"chat", "speech", "streaming_speech", "transcription"} {
				t.Run(modality, func(t *testing.T) {
					wire, err := invokeGenerationModel(t, test.backend, modality, test.raw)
					if err == nil || len(wire) != 1 || !bytes.Contains(wire[0], []byte(test.want)) {
						t.Fatalf("wire=%q error=%v; want %s", wire, err, test.want)
					}
				})
			}
		})
	}
}

func TestGenerationRejectsSDKUnsupportedFieldsBeforeHTTP(t *testing.T) {
	for _, tc := range []struct {
		name    string
		backend genai.Backend
		raw     string
	}{
		{"safety-method", genai.BackendGeminiAPI, `{"safetySettings":[{"category":"HARM_CATEGORY_HATE_SPEECH","threshold":"BLOCK_MEDIUM_AND_ABOVE","method":"SEVERITY"}]}`},
		{"tool-retrieval", genai.BackendGeminiAPI, `{"tools":[{"retrieval":{}}]}`},
		{"tool-enterpriseWebSearch", genai.BackendGeminiAPI, `{"tools":[{"enterpriseWebSearch":{}}]}`},
		{"tool-parallelAiSearch", genai.BackendGeminiAPI, `{"tools":[{"parallelAiSearch":{}}]}`},
		{"tool-exaAiSearch", genai.BackendGeminiAPI, `{"tools":[{"exaAiSearch":{}}]}`},
		{"googleSearch-blockingConfidence", genai.BackendGeminiAPI, `{"tools":[{"googleSearch":{"blockingConfidence":"BLOCK_HIGH_AND_ABOVE"}}]}`},
		{"googleSearch-excludeDomains", genai.BackendGeminiAPI, `{"tools":[{"googleSearch":{"excludeDomains":["example.test"]}}]}`},
		{"googleMaps-groundingTypes", genai.BackendGeminiAPI, `{"tools":[{"googleMaps":{"groundingTypes":{"places":{}}}}]}`},
		{"googleMaps-auth-apiKeyConfig", genai.BackendGeminiAPI, `{"tools":[{"googleMaps":{"authConfig":{"apiKeyConfig":{}}}}]}`},
		{"googleMaps-auth-authType", genai.BackendGeminiAPI, `{"tools":[{"googleMaps":{"authConfig":{"authType":"API_KEY_AUTH"}}}]}`},
		{"googleMaps-auth-googleServiceAccountConfig", genai.BackendGeminiAPI, `{"tools":[{"googleMaps":{"authConfig":{"googleServiceAccountConfig":{}}}}]}`},
		{"googleMaps-auth-httpBasicAuthConfig", genai.BackendGeminiAPI, `{"tools":[{"googleMaps":{"authConfig":{"httpBasicAuthConfig":{}}}}]}`},
		{"googleMaps-auth-oauthConfig", genai.BackendGeminiAPI, `{"tools":[{"googleMaps":{"authConfig":{"oauthConfig":{}}}}]}`},
		{"googleMaps-auth-oidcConfig", genai.BackendGeminiAPI, `{"tools":[{"googleMaps":{"authConfig":{"oidcConfig":{}}}}]}`},
		{"functionCallingConfig-streamFunctionCallArguments", genai.BackendGeminiAPI, `{"toolConfig":{"functionCallingConfig":{"streamFunctionCallArguments":true}}}`},
		{"system-functionCall-partialArgs", genai.BackendGeminiAPI, `{"systemInstruction":{"parts":[{"functionCall":{"name":"f","partialArgs":[{"jsonPath":"$.x","stringValue":"v"}]}}]}}`},
		{"system-functionCall-willContinue", genai.BackendGeminiAPI, `{"systemInstruction":{"parts":[{"functionCall":{"name":"f","willContinue":true}}]}}`},
		{"tool-fileSearch", genai.BackendVertexAI, `{"tools":[{"fileSearch":{}}]}`},
		{"tool-mcpServers-name", genai.BackendVertexAI, `{"tools":[{"mcpServers":[{"name":"audit"}]}]}`},
		{"tool-mcpServers-transport", genai.BackendVertexAI, `{"tools":[{"mcpServers":[{"streamableHttpTransport":{"url":"https://tool.example.test"}}]}]}`},
		{"toolConfig-serverInvocations", genai.BackendVertexAI, `{"toolConfig":{"includeServerSideToolInvocations":false}}`},
		{"system-toolCall", genai.BackendVertexAI, `{"systemInstruction":{"parts":[{"toolCall":{"id":"t","toolType":"GOOGLE_SEARCH_WEB"}}]}}`},
		{"system-toolResponse", genai.BackendVertexAI, `{"systemInstruction":{"parts":[{"toolResponse":{"id":"t","toolType":"GOOGLE_SEARCH_WEB"}}]}}`},
		{"system-partMetadata", genai.BackendVertexAI, `{"systemInstruction":{"parts":[{"text":"audit","partMetadata":{"a":"b"}}]}}`},
		{"system-functionResponse-willContinue", genai.BackendVertexAI, `{"systemInstruction":{"parts":[{"functionResponse":{"name":"f","response":{"output":"audit"},"willContinue":false}}]}}`},
		{"system-functionResponse-mediaProcessing", genai.BackendVertexAI, `{"systemInstruction":{"parts":[{"functionResponse":{"name":"f","response":{"output":"audit"},"parts":[{"fileData":{"fileUri":"gs://audit/x.mp4","mimeType":"video/mp4"},"mediaProcessing":"STATIC"}]}}]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire, sdkErr := invokeGenerationSDK(t, tc.backend, tc.raw)
			if len(wire) != 0 || sdkErr == nil {
				t.Fatalf("SDK did not reject before HTTP: wire=%q err=%v", wire, sdkErr)
			}
			t.Logf("locked full SDK rejection: %v", sdkErr)
			for _, modality := range []string{"chat", "speech", "streaming_speech", "transcription"} {
				t.Run(modality, func(t *testing.T) {
					wire, err := invokeGenerationModel(t, tc.backend, modality, tc.raw)
					if len(wire) != 0 || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted {
						t.Fatalf("SDK-rejected config reached %s: wire=%q err=%v", modality, wire, err)
					}
				})
			}
		})
	}
}

func TestVertexReplayUsesSDKBackendAdmission(t *testing.T) {
	transport := &generationCaptureTransport{}
	model, err := vertexai.NewChat(t.Context(), vertexai.ChatConfig{Client: vertexai.ClientConfig{Project: "test", Location: "global", BaseURL: "https://audit.example.test", HTTPClient: &http.Client{Transport: transport}}, DefaultOptions: chat.Options{Model: google.ModelGemini36Flash}})
	if err != nil {
		t.Fatal(err)
	}
	part := chat.NewTextPart("assistant history")
	if metadataErr := part.Metadata.Set(vertexai.Provider+"/part_state", map[string]any{"partMetadata": map[string]any{"a": "b"}}); metadataErr != nil {
		t.Fatal(metadataErr)
	}
	_, err = model.Call(t.Context(), &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("audit")), chat.NewAssistantMessage(part), chat.NewUserMessage(chat.NewTextPart("continue"))}})
	if len(transport.bodies) != 0 || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted {
		t.Fatalf("wire=%q err=%v", transport.bodies, err)
	}
}
