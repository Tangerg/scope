package mcp

import (
	"errors"
	"fmt"
	"mime"
	"net/url"
	"path"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
)

var errNilPromptContent = errors.New("prompt content is nil")

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

		part, present, err := promptContentToPart(message.Content)
		if err != nil {
			return nil, fmt.Errorf("mcp: prompt message %d: %w", index, err)
		}
		if !present {
			continue
		}

		var converted chat.Message
		switch message.Role {
		case "user":
			converted = chat.NewUserMessage(part)
		case "assistant":
			converted = chat.NewAssistantMessage(part)
		default:
			return nil, fmt.Errorf("mcp: prompt message %d has unsupported role %q", index, message.Role)
		}
		if err := converted.Validate(); err != nil {
			return nil, fmt.Errorf("mcp: prompt message %d: %w", index, err)
		}
		out = append(out, converted)
	}
	return out, nil
}

func promptContentToPart(content sdkmcp.Content) (chat.Part, bool, error) {
	switch value := content.(type) {
	case *sdkmcp.TextContent:
		if value == nil {
			return chat.Part{}, false, errNilPromptContent
		}
		if value.Text == "" {
			return chat.Part{}, false, nil
		}
		return chat.NewTextPart(value.Text), true, nil
	case *sdkmcp.ImageContent:
		if value == nil {
			return chat.Part{}, false, errNilPromptContent
		}
		return promptBytesPart(value.MIMEType, value.Data)
	case *sdkmcp.AudioContent:
		if value == nil {
			return chat.Part{}, false, errNilPromptContent
		}
		return promptBytesPart(value.MIMEType, value.Data)
	case *sdkmcp.ResourceLink:
		if value == nil {
			return chat.Part{}, false, errNilPromptContent
		}
		item, err := media.NewURI(resourceMIME(value.MIMEType, value.URI), value.URI)
		if err != nil {
			return chat.Part{}, false, fmt.Errorf("resource link: %w", err)
		}
		item.Name = value.Name
		return chat.NewMediaPart(item), true, nil
	case *sdkmcp.EmbeddedResource:
		if value == nil || value.Resource == nil {
			return chat.Part{}, false, errNilPromptContent
		}
		resource := value.Resource
		switch {
		case resource.Text != "":
			return chat.NewTextPart(resource.Text), true, nil
		case len(resource.Blob) > 0:
			return promptBytesPart(resourceMIME(resource.MIMEType, resource.URI), resource.Blob)
		case resource.URI != "":
			item, err := media.NewURI(resourceMIME(resource.MIMEType, resource.URI), resource.URI)
			if err != nil {
				return chat.Part{}, false, fmt.Errorf("embedded resource: %w", err)
			}
			return chat.NewMediaPart(item), true, nil
		default:
			return chat.Part{}, false, errors.New("embedded resource has no text, blob, or URI")
		}
	default:
		return chat.Part{}, false, fmt.Errorf("unsupported prompt content %T", content)
	}
}

func promptBytesPart(mimeType string, data []byte) (chat.Part, bool, error) {
	item, err := media.NewBytes(resourceMIME(mimeType, ""), data)
	if err != nil {
		return chat.Part{}, false, err
	}
	return chat.NewMediaPart(item), true, nil
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
