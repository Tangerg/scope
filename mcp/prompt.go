package mcp

import (
	"fmt"
	"mime"
	"net/url"
	"path"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"

	"github.com/Tangerg/scope/core/chat"
)

// PromptMessagesToChat converts MCP prompt messages into Core chat messages.
// Text, image, audio, resource-link, and embedded-resource content retain
// their semantic shape; malformed or unsupported content returns an error
// instead of disappearing from the prompt.
func PromptMessagesToChat(messages []*sdkmcp.PromptMessage) ([]chat.Message, error) {
	out := make([]chat.Message, 0, len(messages))
	for index, message := range messages {
		if message == nil {
			return nil, fmt.Errorf("mcp: prompt message %d is nil", index)
		}
		var converted chat.Message
		switch message.Role {
		case "user":
			converted = chat.NewUserMessage()
		case "assistant":
			converted = chat.NewAssistantMessage()
		default:
			return nil, fmt.Errorf("mcp: prompt message %d has unsupported role %q", index, message.Role)
		}

		part, present, err := promptContentToPart(message.Content)
		if err != nil {
			return nil, fmt.Errorf("mcp: prompt message %d: %w", index, err)
		}
		if !present {
			continue
		}

		converted.Parts = append(converted.Parts, part)
		if err := converted.Validate(); err != nil {
			return nil, fmt.Errorf("mcp: prompt message %d: %w", index, err)
		}
		out = append(out, converted)
	}
	return out, nil
}

func promptContentToPart(content sdkmcp.Content) (chat.Part, bool, error) {
	part, present, err := mapRemoteContent(content)
	if err != nil || !present {
		return chat.Part{}, present, err
	}
	return chat.Part{Kind: part.Kind, Text: part.Text, Media: part.Media, Citations: part.Citations, Metadata: part.Metadata}, true, nil
}

func resourceMIME(mimeType, uri string) string {
	if mimeType != "" {
		return mimeType
	}
	if parsed, err := url.Parse(uri); err == nil {
		if inferred := mime.TypeByExtension(path.Ext(parsed.Path)); inferred != "" {
			return inferred
		}
	}
	return "application/octet-stream"
}
