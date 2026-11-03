package mcp

import (
	"context"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"

	toolcontract "github.com/Tangerg/scope/core/tool"
)

// RequestMetaFunc derives the MCP _meta sent with one remote call from that
// call. Host identity such as a user is bound into the function when the tools
// are discovered for that user, never read back from the context.
type RequestMetaFunc func(ctx context.Context, invocation toolcontract.Invocation) sdkmcp.Meta
