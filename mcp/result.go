package mcp

import (
	"encoding/json"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/tool"
)

type remoteResult struct {
	remoteName string
	value      *sdkmcp.CallToolResult
}

func (r remoteResult) unwrap() (chat.ToolOutput, error) {
	if r.value == nil {
		return chat.ToolOutput{}, fmt.Errorf("mcp: call tool %q: server returned a nil result", r.remoteName)
	}
	output, err := r.content()
	if err != nil {
		return chat.ToolOutput{}, err
	}
	if !r.value.IsError {
		return output, nil
	}
	cause := fmt.Errorf("mcp: tool %q reported failure", r.remoteName)
	if len(output.Content) == 0 && len(output.Details) == 0 {
		output = chat.NewTextToolOutput(cause.Error())
	}
	failure, err := tool.NewFailure(cause, output)
	if err != nil {
		return chat.ToolOutput{}, err
	}
	return chat.ToolOutput{}, failure
}

func (r remoteResult) content() (chat.ToolOutput, error) {
	output := chat.ToolOutput{Content: make([]chat.ToolContent, 0, len(r.value.Content))}
	for index := range r.value.Content {
		part, include, err := mapRemoteContent(r.value.Content[index])
		if err != nil {
			return chat.ToolOutput{}, fmt.Errorf("mcp: tool content[%d]: %w", index, err)
		}
		if include {
			output.Content = append(output.Content, part)
		}
	}
	if r.value.StructuredContent != nil {
		encoded, err := json.Marshal(r.value.StructuredContent)
		if err != nil {
			return chat.ToolOutput{}, fmt.Errorf("mcp: encode structured tool content: %w", err)
		}
		output.Details = encoded
	}
	if err := output.Validate(); err != nil {
		return chat.ToolOutput{}, fmt.Errorf("mcp: mapped tool output: %w", err)
	}
	return output, nil
}

func mapRemoteContent(content sdkmcp.Content) (chat.ToolContent, bool, error) {
	switch value := content.(type) {
	case *sdkmcp.TextContent:
		if value.Text == "" {
			return chat.ToolContent{}, false, nil
		}
		return chat.ToolContent{Kind: chat.PartText, Text: value.Text}, true, nil
	case *sdkmcp.ImageContent:
		part, err := remoteBytesMedia(value.MIMEType, value.Data)
		return part, err == nil, err
	case *sdkmcp.AudioContent:
		part, err := remoteBytesMedia(value.MIMEType, value.Data)
		return part, err == nil, err
	case *sdkmcp.ResourceLink:
		if value.MIMEType != "" {
			linked, err := media.NewURI(value.MIMEType, value.URI)
			if err == nil {
				linked.Name = value.Name
				return chat.ToolContent{Kind: chat.PartMedia, Media: linked}, true, nil
			}
		}
	case *sdkmcp.EmbeddedResource:
		if part, include, err := mapEmbeddedResource(value.Resource); include || err != nil {
			return part, include, err
		}
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		return chat.ToolContent{}, false, fmt.Errorf("encode unsupported content %T: %w", content, err)
	}
	return chat.ToolContent{Kind: chat.PartText, Text: string(encoded)}, true, nil
}

func mapEmbeddedResource(resource *sdkmcp.ResourceContents) (chat.ToolContent, bool, error) {
	if resource == nil {
		return chat.ToolContent{}, false, nil
	}
	if resource.Text != "" {
		return chat.ToolContent{Kind: chat.PartText, Text: resource.Text}, true, nil
	}
	if len(resource.Blob) != 0 && resource.MIMEType != "" {
		part, err := remoteBytesMedia(resource.MIMEType, resource.Blob)
		return part, err == nil, err
	}
	if resource.URI == "" || resource.MIMEType == "" {
		return chat.ToolContent{}, false, nil
	}
	linked, err := media.NewURI(resource.MIMEType, resource.URI)
	if err != nil {
		return chat.ToolContent{}, false, nil
	}
	return chat.ToolContent{Kind: chat.PartMedia, Media: linked}, true, nil
}

func remoteBytesMedia(mimeType string, data []byte) (chat.ToolContent, error) {
	value, err := media.NewBytes(mimeType, data)
	if err != nil {
		return chat.ToolContent{}, err
	}
	return chat.ToolContent{Kind: chat.PartMedia, Media: value}, nil
}
