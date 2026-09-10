package mcp

import (
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDescriptorRejectsMissingOrNonObjectSchema(t *testing.T) {
	for _, schema := range []any{nil, "", `{"type":"object"}`, []any{}, map[string]any{"type": "array"}} {
		_, err := newDescriptorSnapshot(sdkmcp.Tool{Name: "remote", InputSchema: schema}, "public")
		require.Error(t, err)
	}
}

func TestDescriptorSnapshotOwnsOnlyProjectedSDKValues(t *testing.T) {
	destructive := false
	original := sdkmcp.Tool{
		Name: "remote", Description: "original",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "string"}}},
		Annotations: &sdkmcp.ToolAnnotations{DestructiveHint: &destructive, OpenWorldHint: new(false)},
	}
	snapshot, err := newDescriptorSnapshot(original, "public")
	require.NoError(t, err)
	original.Name = "mutated"
	original.Description = "mutated"
	original.InputSchema.(map[string]any)["type"] = "array"
	*original.Annotations.DestructiveHint = true
	assert.Equal(t, "remote", snapshot.remoteName)
	assert.Equal(t, "public", snapshot.definition.Name)
	assert.Equal(t, "original", snapshot.definition.Description)
	assert.JSONEq(t, `{"type":"object","properties":{"x":{"type":"string"}}}`, string(snapshot.definition.InputSchema))
	first := snapshot.annotations()
	*first.DestructiveHint = true
	*first.OpenWorldHint = true
	second := snapshot.annotations()
	assert.False(t, *second.DestructiveHint)
	assert.False(t, *second.OpenWorldHint)
}
