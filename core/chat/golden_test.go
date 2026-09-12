package chat_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

func TestRequestGolden(t *testing.T) {
	attachment, err := media.NewURI("image/png", "https://example.com/scope.png")
	if err != nil {
		t.Fatal(err)
	}

	system := chat.NewSystemMessage("Answer precisely.")
	user := chat.NewUserMessage(chat.NewTextPart("What is shown?"), chat.NewMediaPart(attachment))
	assistant := chat.NewAssistantMessage(
		chat.NewReasoningPart("I should inspect the image.", []byte("opaque-signature")),
		chat.NewToolCallPart(chat.ToolCall{ID: "call-1", Name: "inspect_image", Arguments: `{"detail":"high"}`}),
	)
	tool := chat.NewToolMessage(chat.ToolResult{ID: "call-1", Name: "inspect_image", Output: chat.NewTextToolOutput("A scope.")})
	request, err := chat.NewRequest(system, user, assistant, tool)
	if err != nil {
		t.Fatal(err)
	}
	request.Tools = []chat.ToolDefinition{{
		Name:        "inspect_image",
		Description: "Inspect an image",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"detail":{"type":"string"}}}`),
	}}
	format, err := chat.NewOutputFormat(chat.OutputFormatText)
	if err != nil {
		t.Fatal(err)
	}
	request.Options = chat.Options{Model: "provider-model", OutputFormat: &format, Temperature: new(0.2), MaxOutputTokens: new(int64(256))}
	assertChatGolden(t, "request.golden.json", request)
}

func TestResponseGolden(t *testing.T) {
	firstMessage := chat.NewAssistantMessage(chat.NewTextPart("A scope."))
	response, err := chat.NewResponse(
		&chat.Output{Message: &firstMessage, FinishReason: chat.FinishReasonStop, Metadata: &chat.OutputMetadata{}},
		&chat.ResponseMetadata{ID: "response-1", Model: "provider-model"},
	)
	if err != nil {
		t.Fatal(err)
	}
	reasoning := int64(4)
	cacheRead := int64(8)
	response.Metadata.Usage = &chat.Usage{InputTokens: 32, OutputTokens: 12, ReasoningTokens: &reasoning, CacheReadInputTokens: &cacheRead}
	if err := response.Metadata.Extra.Set("openai/system_fingerprint", "fp-1"); err != nil {
		t.Fatal(err)
	}
	if err := response.Output.Metadata.Extra.Set("openai/logprobs", []float64{-0.1, -0.2}); err != nil {
		t.Fatal(err)
	}
	assertChatGolden(t, "response.golden.json", response)
}

func TestGoldenMetadataIsJSONSafe(t *testing.T) {
	value := metadata.Map{}
	if err := value.Set("fixture", "chat"); err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(value); err != nil {
		t.Fatal(err)
	}
}

func assertChatGolden(t *testing.T, name string, value any) {
	t.Helper()
	got, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	want, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("golden mismatch\n--- got ---\n%s--- want ---\n%s", got, want)
	}
}
