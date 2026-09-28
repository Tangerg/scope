package mcp

import (
	"encoding/json"
	"testing"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDescriptorRejectsMissingOrNonObjectSchema(t *testing.T) {
	for _, schema := range []any{nil, "", `{"type":"object"}`, []any{}, map[string]any{"type": "array"}} {
		_, err := newDescriptorSnapshot(sdkmcp.Tool{Name: "remote", InputSchema: schema}, "public")
		require.Error(t, err)
	}
}

func TestDescriptorOutputSchemaPreservesExactNumbers(t *testing.T) {
	snapshot, err := newDescriptorSnapshot(sdkmcp.Tool{Name: "exact", InputSchema: json.RawMessage(`{"type":"object"}`),
		OutputSchema: json.RawMessage(`{"type":"integer","const":9007199254740993}`)}, "exact")
	require.NoError(t, err)
	require.NoError(t, snapshot.outputSchema.Validate([]byte(`9007199254740993`)))
	require.Error(t, snapshot.outputSchema.Validate([]byte(`9007199254740992`)))
}

func TestDescriptorSnapshotOwnsOnlyProjectedSDKValues(t *testing.T) {
	destructive := false
	original := sdkmcp.Tool{
		Name: "remote", Description: "original",
		InputSchema:  map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "string"}}},
		OutputSchema: map[string]any{"type": "integer"},
		Annotations:  &sdkmcp.ToolAnnotations{DestructiveHint: &destructive, OpenWorldHint: new(false)},
	}
	snapshot, err := newDescriptorSnapshot(original, "public")
	require.NoError(t, err)
	original.Name = "mutated"
	original.Description = "mutated"
	original.InputSchema.(map[string]any)["type"] = "array"
	original.OutputSchema.(map[string]any)["type"] = "string"
	*original.Annotations.DestructiveHint = true
	assert.Equal(t, "remote", snapshot.remoteName)
	assert.Equal(t, "public", snapshot.definition.Name)
	assert.Equal(t, "original", snapshot.definition.Description)
	assert.JSONEq(t, `{"type":"object","properties":{"x":{"type":"string"}}}`, string(snapshot.definition.InputSchema))
	require.NoError(t, snapshot.outputSchema.Validate([]byte(`3`)))
	require.Error(t, snapshot.outputSchema.Validate([]byte(`"3"`)))
	first := snapshot.annotations()
	*first.DestructiveHint = true
	*first.OpenWorldHint = true
	second := snapshot.annotations()
	assert.False(t, *second.DestructiveHint)
	assert.False(t, *second.OpenWorldHint)
}
