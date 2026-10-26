package chat_test

import (
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
	ragchat "github.com/Tangerg/scope/rag/chat"
)

func TestCompressionPreservesPublicHistoryAndOmitsReasoning(t *testing.T) {
	image, err := media.NewURI("image/png", "https://example.test/image.png")
	if err != nil {
		t.Fatal(err)
	}
	model := newFakeChatModel("standalone")
	transformer, err := ragchat.NewCompressionTransformer(ragchat.CompressionTransformerConfig{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	query, err := mustQuery(t, "follow up").WithValue(ragchat.HistoryValueKey(), []chat.Message{
		chat.NewUserMessage(chat.NewTextPart("inspect"), chat.NewMediaPart(image)),
		chat.NewAssistantMessage(chat.NewReasoningPart("private reasoning", nil), chat.NewRefusalPart("declined")),
		chat.NewAssistantMessage(chat.NewToolCallPart(chat.ToolCall{ID: "read", Name: "lookup", Arguments: `{}`})),
		{Role: chat.RoleTool, Parts: []chat.Part{chat.NewToolResultPart(chat.ToolResult{ID: "read", Name: "lookup", Output: chat.NewTextToolOutput("public result")})}},
		chat.NewAssistantMessage(chat.NewToolCallPart(chat.ToolCall{ID: "image", Name: "picture", Arguments: `{}`})),
		{Role: chat.RoleTool, Parts: []chat.Part{chat.NewToolResultPart(chat.ToolResult{ID: "image", Name: "picture", Output: chat.ToolOutput{Content: []chat.ToolContent{{Kind: chat.PartMedia, Media: image}}}})}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := transformer.Transform(t.Context(), query)
	if err != nil || result.Text() != "standalone" {
		t.Fatalf("Transform = %q, %v", result.Text(), err)
	}
	for _, want := range []string{
		"user: inspect [media image/png]",
		"assistant: [reasoning omitted] declined",
		"assistant: [tool call lookup {}]",
		"tool: [tool result lookup public result]",
		"tool: [tool result picture contains media]",
	} {
		if !strings.Contains(model.lastPrompt(), want) {
			t.Errorf("missing public history projection %q in %q", want, model.lastPrompt())
		}
	}
	if strings.Contains(model.lastPrompt(), "private reasoning") {
		t.Fatal("compression exposed reasoning")
	}
}
