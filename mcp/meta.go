package mcp

import (
	"context"
	"maps"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"
)

// RequestMetaFunc resolves request-scoped metadata without rebuilding tools.
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

// RequestMetaFromContext returns a shallow copy, or nil. Nested values remain
// shared and must be kept immutable. It can be used as [RequestMetaFunc].
func RequestMetaFromContext(ctx context.Context) sdkmcp.Meta {
	meta, _ := ctx.Value(requestMetaContextKey{}).(sdkmcp.Meta)
	return maps.Clone(meta)
}
