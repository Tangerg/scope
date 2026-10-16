package mcp

import (
	"context"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"

	"github.com/Tangerg/scope/core/metadata"
)

// RequestMetaFunc resolves request-scoped metadata without rebuilding tools.
type RequestMetaFunc func(ctx context.Context) sdkmcp.Meta

type requestMetaContextKey struct{}

// WithRequestMeta snapshots the metadata map and its encoded values.
func WithRequestMeta(ctx context.Context, meta sdkmcp.Meta) context.Context {
	if len(meta) == 0 {
		return ctx
	}
	return context.WithValue(ctx, requestMetaContextKey{}, sdkmcp.Meta(metadata.Map(meta).Clone()))
}

// RequestMetaFromContext returns an independent copy, or nil.
// It can be used as [RequestMetaFunc].
func RequestMetaFromContext(ctx context.Context) sdkmcp.Meta {
	meta, _ := ctx.Value(requestMetaContextKey{}).(sdkmcp.Meta)
	return sdkmcp.Meta(metadata.Map(meta).Clone())
}
