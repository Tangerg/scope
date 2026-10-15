package mcp

import (
	sdkmcp "github.com/Tangerg/go-sdk/mcp"

	toolcontract "github.com/Tangerg/scope/core/tool"
)

var _ ToolConcurrencyPolicy = AnnotatedReadOnlyConcurrencyPolicy

// AnnotatedReadOnlyConcurrencyPolicy declares no conflict when readOnlyHint is
// true and destructiveHint is absent or false. The Host must trust those hints
// to describe the tool's effects; the classifier neither orders remote
// execution nor authorizes calls.
func AnnotatedReadOnlyConcurrencyPolicy(_, _ string, annotations sdkmcp.ToolAnnotations, _ toolcontract.Invocation) (key string, concurrent bool) {
	if !annotations.ReadOnlyHint {
		return "", false
	}
	if destructive := annotations.DestructiveHint; destructive != nil && *destructive {
		return "", false
	}
	return "", true
}
