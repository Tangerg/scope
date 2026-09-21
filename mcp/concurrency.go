package mcp

import (
	sdkmcp "github.com/Tangerg/go-sdk/mcp"

	toolcontract "github.com/Tangerg/scope/core/tool"
)

var _ ToolConcurrencyPolicy = AnnotatedReadOnlyConcurrencyPolicy

// AnnotatedReadOnlyConcurrencyPolicy opts explicitly read-only MCP tools into
// conflict-free concurrent execution. Missing, false, or contradictory
// annotations remain exclusive.
//
// This is only scheduling advice: it neither authorizes a call nor bypasses a
// caller's approval policy. MCP annotations are untrusted hints, so callers
// should use this policy only for servers whose descriptors they are willing
// to trust for execution ordering.
func AnnotatedReadOnlyConcurrencyPolicy(_, _ string, annotations sdkmcp.ToolAnnotations, _ toolcontract.Invocation) (key string, concurrent bool) {
	if !annotations.ReadOnlyHint {
		return "", false
	}
	if destructive := annotations.DestructiveHint; destructive != nil && *destructive {
		return "", false
	}
	return "", true
}
