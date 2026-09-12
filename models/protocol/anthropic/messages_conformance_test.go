package anthropic_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/protocol/anthropic"
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
	adapter, err := anthropic.NewMessages(t.Context(), anthropic.MessagesConfig{
		APIKey:         "test-key",
		DefaultOptions: corechat.Options{Model: "default-must-be-overridden"},
		BaseURL:        server.URL,
	})
	if err != nil {
		t.Fatalf("NewMessages: %v", err)
	}
	return adapter, adapter
}

func assertProtocolChatCall(t *testing.T, response *corechat.Response) {
	t.Helper()
	if response.Metadata.ID != "msg-1" || response.Metadata.Model != "claude-opus-4-6" {
		t.Fatalf("identity = %q/%q", response.Metadata.ID, response.Metadata.Model)
	}
	result := response.Output
	if result.FinishReason != corechat.FinishReasonToolCalls || result.Message == nil {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Message.Parts) != 4 {
		t.Fatalf("parts = %#v", result.Message.Parts)
	}
	reasoning := result.Message.Parts[0]
	if reasoning.Kind != corechat.PartReasoning || reasoning.Text != "compare the evidence" || string(reasoning.ReasoningState) != "sig-response" {
		t.Errorf("reasoning = %#v", reasoning)
	}
	redacted := result.Message.Parts[1]
	kind, found, err := anthropic.ReasoningBlockKindOf(redacted)
	if err != nil || !found || kind != anthropic.ReasoningBlockRedacted || string(redacted.ReasoningState) != "opaque-redacted-block" {
		t.Errorf("redacted reasoning = %#v/%q/%v/%v", redacted, kind, found, err)
	}
	text := result.Message.Parts[2]
	if len(text.Citations) != 2 || text.Citations[0].Source.Value != "file-1" || text.Citations[1].Source.Value != "https://example.com/source" {
		t.Errorf("citations = %#v", text.Citations)
	}
	call := result.Message.Parts[3].ToolCall
	if call == nil || call.ID != "toolu-2" || call.Name != "lookup" || call.Arguments != `{"id":8}` {
		t.Errorf("tool call = %#v", call)
	}
	usage := response.Metadata.Usage
	if usage.InputTokens != 160 || usage.OutputTokens != 30 ||
		usage.ReasoningTokens == nil || *usage.ReasoningTokens != 10 ||
		usage.CacheReadInputTokens == nil || *usage.CacheReadInputTokens != 40 ||
		usage.CacheWriteInputTokens == nil || *usage.CacheWriteInputTokens != 20 {
		t.Errorf("usage = %#v", usage)
	}
}

func assertProtocolChatStream(t *testing.T, responses []*corechat.ResponseDelta) {
	t.Helper()
	var text, reasoning, signature strings.Builder
	var toolIDs []string
	var citations []corechat.Citation
	var sawRedacted bool
	var finalUsage *corechat.Usage
	for _, response := range responses {
		finalUsage = response.Metadata.Usage
		for _, part := range response.Parts {
			switch part.Kind {
			case corechat.PartDeltaText:
				text.WriteString(part.Text)
			case corechat.PartDeltaReasoning:
				if part.Text == "" && string(part.ReasoningState) == "opaque-stream" {
					sawRedacted = true
					continue
				}
				reasoning.WriteString(part.Text)
				signature.Write(part.ReasoningState)
			case corechat.PartDeltaToolCall:
				toolIDs = append(toolIDs, part.ToolCall.ID)
			case corechat.PartDeltaCitation:
				citations = append(citations, *part.Citation)
			}
		}
	}
	if text.String() != "need another lookup" || reasoning.String() != "compare evidence" || signature.String() != "sig-stream" {
		t.Errorf("stream text/reasoning/signature = %q/%q/%q", text.String(), reasoning.String(), signature.String())
	}
	if !sawRedacted {
		t.Error("stream did not preserve redacted reasoning")
	}
	if len(toolIDs) != 3 {
		t.Fatalf("tool deltas = %v", toolIDs)
	}
	if len(citations) != 1 || citations[0].Source.Value != "search-result-1" {
		t.Errorf("stream citations = %#v", citations)
	}
	for _, id := range toolIDs {
		if id != "toolu-stream" {
			t.Errorf("unstable tool ID %q", id)
		}
	}
	if finalUsage.InputTokens != 160 || finalUsage.OutputTokens != 30 || finalUsage.ReasoningTokens == nil || *finalUsage.ReasoningTokens != 10 {
		t.Errorf("final usage = %#v", finalUsage)
	}
}

func assertProtocolChatAggregated(t *testing.T, response *corechat.Response) {
	t.Helper()
	if response.Metadata.ID != "msg-stream" || response.Metadata.Model != "claude-opus-4-6" || response.Output == nil {
		t.Fatalf("aggregated response = %#v", response)
	}
	result := response.Output
	if result.Message == nil || len(result.Message.Parts) != 4 || result.FinishReason != corechat.FinishReasonToolCalls {
		t.Fatalf("aggregated result = %#v", result)
	}
	reasoning := result.Message.Parts[0]
	redacted := result.Message.Parts[1]
	call := result.Message.Parts[3].ToolCall
	if reasoning.Text != "compare evidence" || string(reasoning.ReasoningState) != "sig-stream" || string(redacted.ReasoningState) != "opaque-stream" || result.Message.Parts[2].Text != "need another lookup" || len(result.Message.Parts[2].Citations) != 1 ||
		call == nil || call.ID != "toolu-stream" || call.Arguments != `{"id":9}` {
		t.Errorf("aggregated parts = %#v; call = %#v", result.Message.Parts, call)
	}
	if response.Metadata.Usage.InputTokens != 160 || response.Metadata.Usage.OutputTokens != 30 {
		t.Errorf("aggregated usage = %#v", response.Metadata.Usage)
	}
}

func newProtocolChatRequest(t *testing.T) *corechat.Request {
	t.Helper()
	image, err := media.NewURI("image/jpeg", "https://example.com/image.jpg")
	if err != nil {
		t.Fatalf("NewURI: %v", err)
	}
	pdf, err := media.NewBytes("application/pdf", []byte("pdf"))
	if err != nil {
		t.Fatalf("NewBytes: %v", err)
	}
	pdf.Name = "paper.pdf"

	thinking, err := anthropic.NewThinkingPart("need a lookup", []byte("sig-anthropic"))
	if err != nil {
		t.Fatalf("NewThinkingPart: %v", err)
	}
	redacted, err := anthropic.NewRedactedThinkingPart([]byte("prior-opaque-block"))
	if err != nil {
		t.Fatalf("NewRedactedThinkingPart: %v", err)
	}
	assistant := corechat.NewAssistantMessage(
		redacted,
		thinking,
		corechat.NewTextPart("I need one fact."),
		corechat.NewToolCallPart(corechat.ToolCall{ID: "toolu-1", Name: "lookup", Arguments: `{"id":7}`}),
	)

	request, err := corechat.NewRequest(
		corechat.NewSystemMessage("Follow policy."),
		corechat.NewUserMessage(corechat.NewTextPart("Read the image and PDF."), corechat.NewMediaPart(image), corechat.NewMediaPart(pdf)),
		assistant,
		corechat.NewToolMessage(corechat.ToolResult{
			ID: "toolu-1", Name: "lookup", Output: corechat.NewTextToolOutput("not found"), IsError: true,
		}),
	)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	maxTokens := int64(1024)
	temperature := 0.2
	request.Options = corechat.Options{
		Model:           "claude-opus-4-6",
		MaxOutputTokens: &maxTokens,
		Stop:            []string{"END"},
		Temperature:     &temperature,
	}
	request.Tools = []corechat.ToolDefinition{{
		Name:        "lookup",
		Description: "Look up a record",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer"}}}`),
	}}
	if err := request.Options.Extensions.Set(anthropic.RequestExtensionKey, map[string]any{
		"thinking":      map[string]any{"type": "enabled", "budget_tokens": 512},
		"cache_control": map[string]any{"type": "ephemeral"},
	}); err != nil {
		t.Fatalf("SetExtension: %v", err)
	}
	return request
}

type protocolChatRequestBody struct {
	Model     string `json:"model"`
	MaxTokens int64  `json:"max_tokens"`
	Stream    bool   `json:"stream"`
	Thinking  struct {
		Type         string `json:"type"`
		BudgetTokens int64  `json:"budget_tokens"`
	} `json:"thinking"`
	CacheControl struct {
		Type string `json:"type"`
	} `json:"cache_control"`
	System []struct {
		Text string `json:"text"`
	} `json:"system"`
	Messages []protocolChatRequestMessage `json:"messages"`
	Tools    []struct {
		Name         string `json:"name"`
		CacheControl struct {
			Type string `json:"type"`
		} `json:"cache_control"`
	} `json:"tools"`
}

type protocolChatRequestMessage struct {
	Role    string                       `json:"role"`
	Content []protocolChatRequestContent `json:"content"`
}

type protocolChatRequestContent struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Source struct {
		Type string `json:"type"`
		URL  string `json:"url"`
		Data string `json:"data"`
	} `json:"source"`
	CacheControl struct {
		Type string `json:"type"`
	} `json:"cache_control"`
	Signature string `json:"signature"`
	Data      string `json:"data"`
	IsError   bool   `json:"is_error"`
	ToolUseID string `json:"tool_use_id"`
}

func newProtocolChatServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		serveProtocolChat(t, writer, request)
	}))
}

func serveProtocolChat(t *testing.T, writer http.ResponseWriter, request *http.Request) {
	t.Helper()
	var body protocolChatRequestBody
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		t.Errorf("decode request: %v", err)
		http.Error(writer, "bad request", http.StatusBadRequest)
		return
	}
	if !assertProtocolRequestEnvelope(t, request, body) {
		http.Error(writer, "unexpected request", http.StatusBadRequest)
		return
	}
	assertProtocolRequestMessages(t, body)
	if body.Stream {
		writeProtocolChatStream(writer)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	fmt.Fprint(writer, protocolChatResponseJSON)
}

func assertProtocolRequestEnvelope(t *testing.T, request *http.Request, body protocolChatRequestBody) bool {
	t.Helper()
	if request.URL.Path != "/v1/messages" || request.Header.Get("x-api-key") != "test-key" || request.Header.Get("anthropic-version") == "" {
		t.Errorf("request identity = %q/%q/%q", request.URL.Path, request.Header.Get("x-api-key"), request.Header.Get("anthropic-version"))
	}
	if body.Model != "claude-opus-4-6" || body.MaxTokens != 1024 || len(body.System) != 1 || len(body.Messages) != 3 || len(body.Tools) != 1 {
		t.Errorf("request shape = model %q max %d system %d messages %d tools %d", body.Model, body.MaxTokens, len(body.System), len(body.Messages), len(body.Tools))
		return false
	}
	if body.Thinking.Type != "enabled" || body.Thinking.BudgetTokens != 512 {
		t.Errorf("thinking = %#v", body.Thinking)
	}
	if body.CacheControl.Type != "ephemeral" || body.Tools[0].CacheControl.Type != "" {
		t.Errorf("cache control = top-level %q/tool %q", body.CacheControl.Type, body.Tools[0].CacheControl.Type)
	}
	return true
}

func assertProtocolRequestMessages(t *testing.T, body protocolChatRequestBody) {
	t.Helper()
	user := body.Messages[0].Content
	if len(user) != 3 || user[1].Type != "image" || user[1].Source.Type != "url" || user[1].Source.URL == "" ||
		user[2].Type != "document" || user[2].Source.Type != "base64" || user[2].Source.Data == "" || user[2].Title != "paper.pdf" {
		t.Errorf("user media blocks = %#v", user)
	}
	assistant := body.Messages[1].Content
	if len(assistant) != 4 || assistant[0].Type != "redacted_thinking" || assistant[0].Data != "prior-opaque-block" || assistant[1].Signature != "sig-anthropic" {
		t.Errorf("assistant blocks = %#v", assistant)
	}
	toolResult := body.Messages[2].Content
	if len(toolResult) != 1 || toolResult[0].Type != "tool_result" || !toolResult[0].IsError || toolResult[0].ToolUseID != "toolu-1" {
		t.Errorf("tool result = %#v", toolResult)
	}
	if toolResult[0].CacheControl.Type != "" {
		t.Errorf("tool result cache control = %q", toolResult[0].CacheControl.Type)
	}
}

func writeProtocolChatStream(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "text/event-stream")
	events := []struct {
		name string
		data string
	}{
		{"message_start", `{"type":"message_start","message":{"id":"msg-stream","type":"message","role":"assistant","model":"claude-opus-4-6","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":100,"output_tokens":0,"cache_read_input_tokens":40,"cache_creation_input_tokens":20}}}`},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"compare evidence"}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-stream"}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		{"content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"opaque-stream"}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":1}`},
		{"content_block_start", `{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"need another "}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"lookup"}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"citations_delta","citation":{"type":"search_result_location","cited_text":"lookup quote","end_block_index":1,"search_result_index":0,"source":"search-result-1","start_block_index":0,"title":"Lookup source"}}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":2}`},
		{"content_block_start", `{"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"toolu-stream","name":"lookup","input":{}}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"{\"id\":"}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"9}"}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":3}`},
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":"END"},"usage":{"output_tokens":30,"output_tokens_details":{"thinking_tokens":10}}}`},
		{"message_stop", `{"type":"message_stop"}`},
	}
	for _, event := range events {
		fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", event.name, event.data)
	}
}

const protocolChatResponseJSON = `{
  "id":"msg-1",
  "type":"message",
  "role":"assistant",
  "model":"claude-opus-4-6",
  "content":[
    {"type":"thinking","thinking":"compare the evidence","signature":"sig-response"},
    {"type":"redacted_thinking","data":"opaque-redacted-block"},
    {"type":"text","text":"I need another lookup.","citations":[
      {"type":"char_location","cited_text":"document quote","document_index":0,"document_title":"Paper","end_char_index":14,"file_id":"file-1","start_char_index":0},
      {"type":"web_search_result_location","cited_text":"web quote","encrypted_index":"encrypted","title":"Web source","url":"https://example.com/source"}
    ]},
    {"type":"tool_use","id":"toolu-2","name":"lookup","input":{"id":8}}
  ],
  "stop_reason":"tool_use",
  "stop_sequence":"END",
  "usage":{
    "input_tokens":100,
    "output_tokens":30,
    "cache_read_input_tokens":40,
    "cache_creation_input_tokens":20,
    "output_tokens_details":{"thinking_tokens":10}
  }
}`
