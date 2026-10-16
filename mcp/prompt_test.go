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
		{name: "unknown role with empty text", message: &sdkmcp.PromptMessage{Role: "system", Content: &sdkmcp.TextContent{}}},
		{name: "missing role with empty text", message: &sdkmcp.PromptMessage{Content: &sdkmcp.TextContent{}}},
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

func TestPromptMessagesToChatCoversEveryProtocolShape(t *testing.T) {
	for _, test := range []struct {
		name    string
		content sdkmcp.Content
		kind    chat.PartKind
	}{
		{name: "text", content: &sdkmcp.TextContent{Text: "hello"}, kind: chat.PartText},
		{name: "empty text is dropped", content: &sdkmcp.TextContent{}},
		{name: "image", content: &sdkmcp.ImageContent{MIMEType: "image/png", Data: []byte("\x89PNG")}, kind: chat.PartMedia},
		{name: "audio", content: &sdkmcp.AudioContent{MIMEType: "audio/mpeg", Data: []byte("\xFF")}, kind: chat.PartMedia},
		{name: "resource link", content: &sdkmcp.ResourceLink{MIMEType: "image/png", URI: "https://example.com/a.png"}, kind: chat.PartMedia},
		{name: "embedded text", content: &sdkmcp.EmbeddedResource{Resource: &sdkmcp.ResourceContents{URI: "file:///inline", Text: "inline"}}, kind: chat.PartText},
		{name: "embedded blob", content: &sdkmcp.EmbeddedResource{Resource: &sdkmcp.ResourceContents{URI: "file:///image", MIMEType: "image/png", Blob: []byte("\x89PNG")}}, kind: chat.PartMedia},
	} {
		t.Run(test.name, func(t *testing.T) {
			messages, err := scopemcp.PromptMessagesToChat([]*sdkmcp.PromptMessage{{Role: "user", Content: test.content}})
			if err != nil {
				t.Fatal(err)
			}
			if test.kind == "" {
				if len(messages) != 0 {
					t.Fatalf("empty content produced messages: %#v", messages)
				}
				return
			}
			if len(messages) != 1 || len(messages[0].Parts) != 1 {
				t.Fatalf("expected one message with one part: %#v", messages)
			}
			if messages[0].Role != chat.RoleUser || messages[0].Parts[0].Kind != test.kind {
				t.Fatalf("message = %#v, want user with %s part", messages[0], test.kind)
			}
		})
	}
}

func TestPromptMessagesToChatRejectsUnusableContent(t *testing.T) {
	for name, content := range map[string]sdkmcp.Content{
		"nil text":          (*sdkmcp.TextContent)(nil),
		"nil image":         (*sdkmcp.ImageContent)(nil),
		"nil audio":         (*sdkmcp.AudioContent)(nil),
		"nil resource link": (*sdkmcp.ResourceLink)(nil),
		"nil embedded":      (*sdkmcp.EmbeddedResource)(nil),
		"empty embedded":    &sdkmcp.EmbeddedResource{Resource: &sdkmcp.ResourceContents{}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := scopemcp.PromptMessagesToChat([]*sdkmcp.PromptMessage{{Role: "user", Content: content}}); err == nil {
				t.Fatal("unusable prompt content was accepted")
			}
		})
	}
}
