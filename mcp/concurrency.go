package mcp

import (
	sdkmcp "github.com/Tangerg/go-sdk/mcp"

	toolcontract "github.com/Tangerg/scope/core/tool"
)

var _ ToolConcurrencyPolicy = AnnotatedReadOnlyConcurrencyPolicy

// Only the host can establish whether remote hints describe actual effects.
// Classification neither orders execution nor authorizes calls.
func AnnotatedReadOnlyConcurrencyPolicy(_, _ string, annotations sdkmcp.ToolAnnotations, _ toolcontract.Invocation) (key string, concurrent bool) {
	if !annotations.ReadOnlyHint {
		return "", false
	}
	if destructive := annotations.DestructiveHint; destructive != nil && *destructive {
		return "", false
	}
	return "", true
}
