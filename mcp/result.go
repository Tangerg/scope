package mcp

import (
	jsonv2 "encoding/json/v2"
	"fmt"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/tool"
)

type remoteResult struct {
	remoteName   string
	value        *sdkmcp.CallToolResult
	outputSchema jsonschema.Schema
}

func (r remoteResult) unwrap() (chat.ToolOutput, error) {
	if r.value == nil {
		return chat.ToolOutput{}, fmt.Errorf("mcp: call tool %q: server returned a nil result", r.remoteName)
	}
	if r.value.NeedsInput() {
		return chat.ToolOutput{}, fmt.Errorf("mcp: call tool %q: %w", r.remoteName, ErrIncompleteResult)
	}
	output, err := r.content()
	if err != nil {
		return chat.ToolOutput{}, err
	}
	if !r.value.IsError {
		if r.outputSchema.Valid() {
			if validationErr := r.outputSchema.Validate(output.Details); validationErr != nil {
				return chat.ToolOutput{}, fmt.Errorf("mcp: tool %q structured content does not match its output schema: %w", r.remoteName, validationErr)
			}
		}
		return output, nil
	}
	cause := fmt.Errorf("mcp: tool %q reported failure", r.remoteName)
	if len(output.Content) == 0 && len(output.Details) == 0 {
		output = chat.NewTextToolOutput(cause.Error())
	}
	failure, err := tool.NewFailure(tool.FailureConfig{Kind: tool.FailureKindFailed, Cause: cause, Output: output})
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
		encoded, err := jsonv2.Marshal(r.value.StructuredContent)
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
