package mcp

import (
	"encoding/json"
	"errors"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	corechat "github.com/Tangerg/scope/core/chat"
)

type descriptorSnapshot struct {
	remoteName      string
	definition      corechat.ToolDefinition
	toolAnnotations sdkmcp.ToolAnnotations
}

func newDescriptorSnapshot(descriptor sdkmcp.Tool, publicName string) (descriptorSnapshot, error) {
	if descriptor.Name == "" {
		return descriptorSnapshot{}, errors.New("mcp: descriptor name must not be empty")
	}
	schema, err := json.Marshal(descriptor.InputSchema)
	if err != nil {
		return descriptorSnapshot{}, fmt.Errorf("mcp: encode tool input schema: %w", err)
	}
	definition := corechat.ToolDefinition{
		Name: publicName, Description: descriptor.Description, InputSchema: schema,
	}
	if err := definition.Validate(); err != nil {
		return descriptorSnapshot{}, err
	}
	snapshot := descriptorSnapshot{remoteName: descriptor.Name, definition: definition}
	if descriptor.Annotations != nil {
		snapshot.toolAnnotations = cloneToolAnnotations(*descriptor.Annotations)
	}
	return snapshot, nil
}

func (d descriptorSnapshot) annotations() sdkmcp.ToolAnnotations {
	return cloneToolAnnotations(d.toolAnnotations)
}

func cloneToolAnnotations(annotations sdkmcp.ToolAnnotations) sdkmcp.ToolAnnotations {
	if annotations.DestructiveHint != nil {
		annotations.DestructiveHint = new(*annotations.DestructiveHint)
	}
	if annotations.OpenWorldHint != nil {
		annotations.OpenWorldHint = new(*annotations.OpenWorldHint)
	}
	return annotations
}
