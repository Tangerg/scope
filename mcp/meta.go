package mcp

import (
	"context"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"

	"github.com/Tangerg/scope/core/metadata"
)

type RequestMetaFunc func(ctx context.Context) sdkmcp.Meta

type requestMetaContextKey struct{}

// Callers may reuse metadata buffers while the request is in flight.
func WithRequestMeta(ctx context.Context, meta sdkmcp.Meta) context.Context {
	if len(meta) == 0 {
		return ctx
	}
	return context.WithValue(ctx, requestMetaContextKey{}, sdkmcp.Meta(metadata.Map(meta).Clone()))
}

func RequestMetaFromContext(ctx context.Context) sdkmcp.Meta {
	meta, _ := ctx.Value(requestMetaContextKey{}).(sdkmcp.Meta)
	return sdkmcp.Meta(metadata.Map(meta).Clone())
}
