package protocol_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/google/internal/protocol"
)

func TestChat_CoreConformance(t *testing.T) {
	modeltest.ChatSuite{
		New:              newProtocolChatModel,
		Request:          newProtocolChatRequest,
		AssertCall:       assertProtocolChatCall,
		AssertStream:     assertProtocolChatStream,
		AssertAggregated: assertProtocolChatAggregated,
	}.Run(t)
}

func newProtocolChatModel(t *testing.T) (corechat.Model, corechat.Streamer) {
	t.Helper()
	server := newProtocolChatServer(t)
	t.Cleanup(server.Close)
	adapter, err := protocol.NewChat(t.Context(), protocol.ChatConfig{
		Provider:       "google",
		Client:         protocol.ClientConfig{APIKey: "test-key", BaseURL: server.URL},
		DefaultOptions: corechat.Options{Model: "default-must-be-overridden"},
	})
	if err != nil {
		t.Fatalf("NewChat: %v", err)
	}
	return adapter, adapter
}

func assertProtocolChatCall(t *testing.T, response *corechat.Response) {
	t.Helper()
	if response.Metadata.ID != "response-1" || response.Metadata.Model != "gemini-3-pro-001" {
		t.Fatalf("identity = %q/%q", response.Metadata.ID, response.Metadata.Model)
	}
	result := response.Output
	if result.Message == nil || len(result.Message.Parts) != 3 || result.FinishReason != corechat.FinishReasonToolCalls {
		t.Fatalf("result = %#v", result)
	}
	reasoning := result.Message.Parts[0]
	if reasoning.Kind != corechat.PartReasoning || reasoning.Text != "verify result" || string(reasoning.ReasoningState) != "sig-google" {
		t.Errorf("reasoning = %#v", reasoning)
	}
	call := result.Message.Parts[2].ToolCall
	if call == nil || call.ID != "google/generated/2" || call.Name != "calculate" || call.Arguments != `{"x":2}` {
		t.Errorf("tool call = %#v", call)
	}
	usage := response.Metadata.Usage
	if usage.InputTokens != 23 || usage.OutputTokens != 13 ||
		usage.ReasoningTokens == nil || *usage.ReasoningTokens != 4 ||
		usage.CacheReadInputTokens == nil || *usage.CacheReadInputTokens != 6 {
		t.Errorf("usage = %#v", usage)
	}
}

func assertProtocolChatStream(t *testing.T, responses []*corechat.ResponseDelta) {
	t.Helper()
	var text, reasoning strings.Builder
	var signature []byte
	var toolID string
	var finalUsage *corechat.Usage
	for _, response := range responses {
		finalUsage = response.Metadata.Usage
		for _, part := range response.Parts {
			switch part.Kind {
			case corechat.PartDeltaText:
				text.WriteString(part.Text)
			case corechat.PartDeltaReasoning:
				reasoning.WriteString(part.Text)
				signature = append(signature, part.ReasoningState...)
			case corechat.PartDeltaToolCall:
				toolID = part.ToolCall.ID
			}
		}
	}
	if reasoning.String() != "verify result" || string(signature) != "sig-google" || text.String() != "The value is four." {
		t.Errorf("stream reasoning/signature/text = %q/%q/%q", reasoning.String(), signature, text.String())
	}
	if toolID != "google/generated/2" {
		t.Errorf("synthetic stream tool ID = %q", toolID)
	}
	if finalUsage.InputTokens != 23 || finalUsage.OutputTokens != 13 {
		t.Errorf("final usage = %#v", finalUsage)
	}
}

func assertProtocolChatAggregated(t *testing.T, response *corechat.Response) {
	t.Helper()
	if response.Metadata.ID != "response-stream" || response.Metadata.Model != "gemini-3-pro-001" || response.Output == nil {
		t.Fatalf("aggregated response = %#v", response)
	}
	result := response.Output
	if result.Message == nil || len(result.Message.Parts) != 3 || result.FinishReason != corechat.FinishReasonToolCalls {
		t.Fatalf("aggregated result = %#v", result)
	}
	call := result.Message.Parts[2].ToolCall
	if result.Message.Parts[0].Text != "verify result" || string(result.Message.Parts[0].ReasoningState) != "sig-google" ||
		result.Message.Parts[1].Text != "The value is four." || call == nil || call.ID != "google/generated/2" {
		t.Errorf("aggregated parts = %#v", result.Message.Parts)
	}
	if response.Metadata.Usage.InputTokens != 23 || response.Metadata.Usage.OutputTokens != 13 {
		t.Errorf("aggregated usage = %#v", response.Metadata.Usage)
	}
}

func TestChatRejectsMultipleProviderCandidates(t *testing.T) {
	server := modeltest.JSONServer(http.StatusOK, `{
		"responseId":"response-multiple","modelVersion":"gemini-3-pro-001","candidates":[
			{"index":0,"content":{"role":"model","parts":[{"text":"first"}]}},
			{"index":1,"content":{"role":"model","parts":[{"text":"second"}]}}
		]
	}`)
	t.Cleanup(server.Close)
	model, err := protocol.NewChat(t.Context(), protocol.ChatConfig{
		Provider: "google", Client: protocol.ClientConfig{APIKey: "test-key", BaseURL: server.URL},
		DefaultOptions: corechat.Options{Model: "gemini-3-pro"},
	})
	if err != nil {
		t.Fatalf("NewChat: %v", err)
	}
	request, err := corechat.NewRequest(corechat.NewUserMessage(corechat.NewTextPart("hello")))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if _, err := model.Call(t.Context(), request); err == nil || !strings.Contains(err.Error(), "supports one output") {
		t.Fatalf("Call error = %v; want multiple-candidate rejection", err)
	}
}

func newProtocolChatRequest(t *testing.T) *corechat.Request {
	t.Helper()
	image, err := media.NewURI("image/png", "gs://bucket/image.png")
	if err != nil {
		t.Fatalf("NewURI: %v", err)
	}
	audio, err := media.NewBytes("audio/wav", []byte("audio"))
	if err != nil {
		t.Fatalf("NewBytes: %v", err)
	}
	request, err := corechat.NewRequest(
		corechat.NewSystemMessage("Answer with evidence."),
		corechat.NewUserMessage(corechat.NewTextPart("Analyze both inputs."), corechat.NewMediaPart(image), corechat.NewMediaPart(audio)),
		corechat.NewAssistantMessage(
			corechat.NewReasoningPart("use the calculator", []byte("sig-google")),
			corechat.NewTextPart("Calculating."),
			corechat.NewToolCallPart(corechat.ToolCall{ID: "google/0/2", Name: "calculate", Arguments: `{"x":2}`}),
		),
		corechat.NewToolMessage(corechat.ToolResult{
			ID: "google/0/2", Name: "calculate", Output: corechat.NewTextToolOutput(`{"value":4}`),
		}),
	)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	maxTokens := int64(768)
	temperature := 0.4
	topK := int64(32)
	topP := 0.9
	request.Options = corechat.Options{
		Model:           "gemini-3-pro",
		MaxOutputTokens: &maxTokens,
		Stop:            []string{"STOP"},
		Temperature:     &temperature,
		TopK:            &topK,
		TopP:            &topP,
	}
	request.Tools = []corechat.ToolDefinition{{
		Name:        "calculate",
		Description: "Run a calculation",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"x":{"type":"number"}}}`),
	}}
	if err := request.Options.Extensions.Set("google/request", map[string]any{
		"safety_settings": []map[string]any{{
			"category":  "HARM_CATEGORY_DANGEROUS_CONTENT",
			"threshold": "BLOCK_ONLY_HIGH",
		}},
		"response_modalities": []string{"TEXT"},
	}); err != nil {
		t.Fatalf("SetExtension: %v", err)
	}
	return request
}

func newProtocolChatServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			SystemInstruction struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"systemInstruction"`
			Contents []struct {
				Role  string `json:"role"`
				Parts []struct {
					Text             string `json:"text"`
					Thought          bool   `json:"thought"`
					ThoughtSignature string `json:"thoughtSignature"`
					InlineData       struct {
						MIMEType string `json:"mimeType"`
						Data     string `json:"data"`
					} `json:"inlineData"`
					FileData struct {
						MIMEType string `json:"mimeType"`
						FileURI  string `json:"fileUri"`
					} `json:"fileData"`
					FunctionCall struct {
						ID   string         `json:"id"`
						Name string         `json:"name"`
						Args map[string]any `json:"args"`
					} `json:"functionCall"`
					FunctionResponse struct {
						ID       string         `json:"id"`
						Name     string         `json:"name"`
						Response map[string]any `json:"response"`
					} `json:"functionResponse"`
				} `json:"parts"`
			} `json:"contents"`
			GenerationConfig struct {
				MaxOutputTokens    int32    `json:"maxOutputTokens"`
				StopSequences      []string `json:"stopSequences"`
				ResponseModalities []string `json:"responseModalities"`
			} `json:"generationConfig"`
			SafetySettings []struct {
				Category  string `json:"category"`
				Threshold string `json:"threshold"`
			} `json:"safetySettings"`
			Tools []struct {
				FunctionDeclarations []struct {
					Name string `json:"name"`
				} `json:"functionDeclarations"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(writer, "bad request", http.StatusBadRequest)
			return
		}
		if request.Header.Get("x-goog-api-key") != "test-key" || !strings.Contains(request.URL.Path, "gemini-3-pro") {
			t.Errorf("request identity = %q/%q", request.URL.Path, request.Header.Get("x-goog-api-key"))
		}
		if len(body.SystemInstruction.Parts) != 1 || len(body.Contents) != 3 || len(body.Tools) != 1 ||
			body.GenerationConfig.MaxOutputTokens != 768 || strings.Join(body.GenerationConfig.ResponseModalities, ",") != "TEXT" || len(body.SafetySettings) != 1 {
			t.Errorf("request shape = system %d contents %d tools %d config %#v safety %d", len(body.SystemInstruction.Parts), len(body.Contents), len(body.Tools), body.GenerationConfig, len(body.SafetySettings))
		}
		user := body.Contents[0].Parts
		if len(user) != 3 || user[1].FileData.FileURI != "gs://bucket/image.png" || user[2].InlineData.MIMEType != "audio/wav" || user[2].InlineData.Data != base64.StdEncoding.EncodeToString([]byte("audio")) {
			t.Errorf("user media = %#v", user)
		}
		assistant := body.Contents[1].Parts
		if len(assistant) != 3 || !assistant[0].Thought || assistant[0].ThoughtSignature != base64.StdEncoding.EncodeToString([]byte("sig-google")) || assistant[2].FunctionCall.ID != "google/0/2" {
			t.Errorf("assistant parts = %#v", assistant)
		}
		toolResult := body.Contents[2].Parts[0].FunctionResponse
		if toolResult.ID != "google/0/2" || toolResult.Name != "calculate" || toolResult.Response["value"] != float64(4) {
			t.Errorf("tool result = %#v", toolResult)
		}
		if strings.Contains(request.URL.Path, "streamGenerateContent") {
			writeProtocolChatStream(writer)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, protocolChatResponseJSON)
	}))
}

func writeProtocolChatStream(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "text/event-stream")
	chunks := []string{
		`{"responseId":"response-stream","modelVersion":"gemini-3-pro-001","candidates":[{"index":0,"content":{"role":"model","parts":[{"thought":true,"text":"verify result","thoughtSignature":"c2lnLWdvb2dsZQ=="}]}}]}`,
		`{"responseId":"response-stream","modelVersion":"gemini-3-pro-001","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"The value is four."}]}}]}`,
		`{"responseId":"response-stream","modelVersion":"gemini-3-pro-001","candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"name":"calculate","args":{"x":2}}}]},"finishReason":"STOP"}]}`,
		`{"responseId":"response-stream","modelVersion":"gemini-3-pro-001","usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":9,"thoughtsTokenCount":4,"toolUsePromptTokenCount":3,"cachedContentTokenCount":6,"totalTokenCount":36}}`,
	}
	for _, chunk := range chunks {
		fmt.Fprintf(writer, "data: %s\n\n", chunk)
	}
}

const protocolChatResponseJSON = `{
  "responseId":"response-1",
  "modelVersion":"gemini-3-pro-001",
  "candidates":[
    {
      "index":0,
      "content":{"role":"model","parts":[
        {"thought":true,"text":"verify result","thoughtSignature":"c2lnLWdvb2dsZQ=="},
        {"text":"The value is four."},
        {"functionCall":{"name":"calculate","args":{"x":2}}}
      ]},
      "finishReason":"STOP"
    }
  ],
  "usageMetadata":{
    "promptTokenCount":20,
    "candidatesTokenCount":9,
    "thoughtsTokenCount":4,
    "toolUsePromptTokenCount":3,
    "cachedContentTokenCount":6,
    "totalTokenCount":36
  }
}`
