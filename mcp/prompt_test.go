package mcp_test

import (
	"testing"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"

	"github.com/Tangerg/scope/core/chat"
	scopemcp "github.com/Tangerg/scope/mcp"
)

func TestPromptMessagesToChatPreservesContentKinds(t *testing.T) {
	messages, err := scopemcp.PromptMessagesToChat([]*sdkmcp.PromptMessage{
		{Role: "user", Content: &sdkmcp.TextContent{Text: "describe this"}},
		{Role: "user", Content: &sdkmcp.ImageContent{MIMEType: "image/png", Data: []byte{1, 2, 3}}},
		{Role: "assistant", Content: &sdkmcp.ResourceLink{URI: "https://example.com/report.pdf", Name: "report"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 {
		t.Fatalf("message count = %d, want 3", len(messages))
	}
	if messages[0].Role != chat.RoleUser || messages[0].Text() != "describe this" {
		t.Fatalf("text message = %#v", messages[0])
	}
	image := messages[1].Parts[0]
	if image.Kind != chat.PartMedia || image.Media.MIME != "image/png" {
		t.Fatalf("image part = %#v", image)
	}
	link := messages[2].Parts[0]
	if messages[2].Role != chat.RoleAssistant || link.Kind != chat.PartMedia || link.Media.MIME != "application/pdf" {
		t.Fatalf("resource link = %#v", messages[2])
	}
	if link.Media.Name != "report" {
		t.Fatalf("resource name = %q, want report", link.Media.Name)
	}
}

func TestPromptMessagesToChatRejectsLossyInput(t *testing.T) {
	tests := []struct {
		name    string
		message *sdkmcp.PromptMessage
	}{
		{name: "nil message"},
		{name: "unknown role", message: &sdkmcp.PromptMessage{Role: "system", Content: &sdkmcp.TextContent{Text: "x"}}},
		{name: "nil content", message: &sdkmcp.PromptMessage{Role: "user"}},
		{name: "empty image", message: &sdkmcp.PromptMessage{Role: "user", Content: &sdkmcp.ImageContent{MIMEType: "image/png"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := scopemcp.PromptMessagesToChat([]*sdkmcp.PromptMessage{test.message}); err == nil {
				t.Fatal("PromptMessagesToChat() error = nil")
			}
		})
	}
}

func TestPromptMessagesToChatSkipsEmptyText(t *testing.T) {
	messages, err := scopemcp.PromptMessagesToChat([]*sdkmcp.PromptMessage{
		{Role: "user", Content: &sdkmcp.TextContent{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("messages = %#v, want empty", messages)
	}
}
