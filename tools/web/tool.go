package web

import (
	"context"

	"github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

type readOnlyTool struct {
	inner toolcontract.Tool
}

func (r readOnlyTool) Definition() chat.ToolDefinition { return r.inner.Definition() }

func (r readOnlyTool) Call(ctx context.Context, invocation toolcontract.Invocation) (chat.ToolOutput, error) {
	return r.inner.Call(ctx, invocation)
}

func (readOnlyTool) ConcurrencyPolicy() func(toolcontract.Invocation) (string, bool) {
	return func(toolcontract.Invocation) (string, bool) { return "", true }
}

func (r readOnlyTool) Unwrap() toolcontract.Tool { return r.inner }
