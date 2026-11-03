package mcp

import (
	jsonv2 "encoding/json/v2"
	"fmt"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"

	corecontent "github.com/Tangerg/scope/core/content"
	"github.com/Tangerg/scope/core/tool"
)

type remoteResult struct {
	remoteName string
	value      *sdkmcp.CallToolResult
}

func (r remoteResult) unwrap() (tool.Output, error) {
	if r.value == nil {
		return tool.Output{}, fmt.Errorf("mcp: call tool %q: server returned a nil result", r.remoteName)
	}
	if r.value.NeedsInput() {
		return tool.Output{}, fmt.Errorf("mcp: call tool %q: %w", r.remoteName, ErrIncompleteResult)
	}
	output, err := r.content()
	if err != nil {
		return tool.Output{}, err
	}
	if r.value.IsError {
		cause := fmt.Errorf("mcp: tool %q reported failure", r.remoteName)
		if len(output.Content) == 0 && len(output.Details) == 0 {
			output = tool.NewTextOutput(cause.Error())
		}
		failure, failureErr := tool.NewFailure(tool.FailureConfig{Kind: tool.FailureKindFailed, Cause: cause, Output: output})
		if failureErr != nil {
			return tool.Output{}, failureErr
		}
		return tool.Output{}, failure
	}

	return output, nil
}

func (r remoteResult) content() (tool.Output, error) {
	output := tool.Output{Content: make([]corecontent.Block, 0, len(r.value.Content))}
	for index := range r.value.Content {
		part, include, err := mapRemoteContent(r.value.Content[index])
		if err != nil {
			return tool.Output{}, fmt.Errorf("mcp: tool content[%d]: %w", index, err)
		}
		if include {
			output.Content = append(output.Content, part)
		}
	}
	if r.value.StructuredContent != nil {
		encoded, err := jsonv2.Marshal(r.value.StructuredContent)
		if err != nil {
			return tool.Output{}, fmt.Errorf("mcp: encode structured tool content: %w", err)
		}
		output.Details = encoded
	}
	if err := output.Validate(); err != nil {
		return tool.Output{}, fmt.Errorf("mcp: mapped tool output: %w", err)
	}
	return output, nil
}
