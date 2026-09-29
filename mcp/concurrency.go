package mcp

import (
	sdkmcp "github.com/Tangerg/go-sdk/mcp"

	toolcontract "github.com/Tangerg/scope/core/tool"
)

var _ ToolConcurrencyPolicy = AnnotatedReadOnlyConcurrencyPolicy

// AnnotatedReadOnlyConcurrencyPolicy lets tools annotated readOnlyHint=true and
// not destructive run concurrently; every other tool stays exclusive. MCP
// annotations are untrusted hints, so use it only for servers trusted to order
// execution. It is scheduling advice, never authorization.
func AnnotatedReadOnlyConcurrencyPolicy(_, _ string, annotations sdkmcp.ToolAnnotations, _ toolcontract.Invocation) (key string, concurrent bool) {
	if !annotations.ReadOnlyHint {
		return "", false
	}
	if destructive := annotations.DestructiveHint; destructive != nil && *destructive {
		return "", false
	}
	return "", true
}
