package mcp

import (
	"errors"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tangerg/scope/core/tool"
)

func TestRemoteResultContent(t *testing.T) {
	tests := []struct {
		name       string
		content    []sdkmcp.Content
		structured any
		wantText   string
		wantParts  int
		wantMedia  bool
	}{
		{name: "empty"},
		{name: "single text", content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "hi"}}, wantText: "hi", wantParts: 1},
		{name: "structured fallback", structured: map[string]any{"answer": 42}, wantText: `{"answer":42}`},
		{
			name:       "content precedes structured fallback",
			content:    []sdkmcp.Content{&sdkmcp.TextContent{Text: "visible"}},
			structured: map[string]any{"answer": 42},
			wantText:   "visible",
			wantParts:  1,
		},
		{
			name: "multiple",
			content: []sdkmcp.Content{
				&sdkmcp.TextContent{Text: "a"},
				&sdkmcp.TextContent{Text: "b"},
			},
			wantText:  "ab",
			wantParts: 2,
		},
		{
			name:      "single non-text",
			content:   []sdkmcp.Content{&sdkmcp.ImageContent{MIMEType: "image/png", Data: []byte{1, 2, 3}}},
			wantParts: 1,
			wantMedia: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := (remoteResult{value: &sdkmcp.CallToolResult{
				Content:           test.content,
				StructuredContent: test.structured,
			}}).content()
			require.NoError(t, err)
			assert.Len(t, got.Content, test.wantParts)
			text, textOK := got.Text()
			if test.wantMedia {
				assert.False(t, textOK)
				assert.Equal(t, "media", string(got.Content[0].Kind))
				return
			}
			assert.True(t, textOK)
			assert.Equal(t, test.wantText, text)
		})
	}
}

func TestRemoteFailureRetainsMediaAndStructuredDetails(t *testing.T) {
	result := remoteResult{remoteName: "inspect", value: &sdkmcp.CallToolResult{
		IsError: true,
		Content: []sdkmcp.Content{
			&sdkmcp.TextContent{Text: "bad image"},
			&sdkmcp.ImageContent{MIMEType: "image/png", Data: []byte{1, 2, 3}},
		},
		StructuredContent: map[string]any{"code": "invalid"},
	}}
	want, err := result.content()
	require.NoError(t, err)
	_, err = result.unwrap()
	failure, ok := errors.AsType[*tool.Failure](err)
	require.True(t, ok)
	assert.Equal(t, want, failure.Output())
}
