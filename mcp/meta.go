package mcp

import (
	"context"
	"maps"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// RequestMetaFunc resolves per-call MCP metadata from the context rather than
// from a fixed value, so a caller can forward request-scoped identity such as a
// trace or tenant without rebuilding the tool for every call.
type RequestMetaFunc func(ctx context.Context) sdkmcp.Meta

type requestMetaContextKey struct{}

// WithRequestMeta copies the top-level map so its keys may be reused or changed
// by the caller. Nested maps, slices, and other reference values remain shared;
// callers must keep them immutable while this context or its derived contexts
// may be read.
func WithRequestMeta(ctx context.Context, meta sdkmcp.Meta) context.Context {
	if len(meta) == 0 {
		return ctx
	}
	return context.WithValue(ctx, requestMetaContextKey{}, maps.Clone(meta))
}

// RequestMetaFromContext returns a shallow copy of metadata stored by
// [WithRequestMeta], or nil. The returned nested values must remain immutable.
// Its signature matches [RequestMetaFunc]:
//
//	config := mcp.ToolDiscoveryConfig{RequestMeta: mcp.RequestMetaFromContext}
func RequestMetaFromContext(ctx context.Context) sdkmcp.Meta {
	meta, _ := ctx.Value(requestMetaContextKey{}).(sdkmcp.Meta)
	return maps.Clone(meta)
}
