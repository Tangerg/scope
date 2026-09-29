package mcp

import (
	"fmt"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"

	"github.com/Tangerg/scope/core/chat"
)

const (
	promptRoleUser      sdkmcp.Role = "user"
	promptRoleAssistant sdkmcp.Role = "assistant"
)

// PromptMessagesToChat converts MCP prompt messages into Core chat messages
// through the shared content codec. Messages whose only content is empty text
// are omitted; malformed or unsupported content is an error.
func PromptMessagesToChat(messages []*sdkmcp.PromptMessage) ([]chat.Message, error) {
	out := make([]chat.Message, 0, len(messages))
	for index, message := range messages {
		if message == nil {
			return nil, fmt.Errorf("mcp: prompt message %d is nil", index)
		}
		var converted chat.Message
		switch message.Role {
		case promptRoleUser:
			converted = chat.NewUserMessage()
		case promptRoleAssistant:
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
