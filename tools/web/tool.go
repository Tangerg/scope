package web

import (
	"context"
	"fmt"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

type readOnlyTool struct {
	inner toolcontract.Tool
}

func newReadOnlyTool[I, O any](config toolcontract.FuncConfig, call func(context.Context, I) (O, error)) (readOnlyTool, error) {
	inner, err := toolcontract.NewFunc(config, call)
	if err != nil {
		return readOnlyTool{}, err
	}
	return readOnlyTool{inner: inner}, nil
}

func newProviderReadOnlyTool[I, P, O any](
	operation string,
	config toolcontract.FuncConfig,
	provider any,
	missingProvider error,
	prepare func(I) (P, error),
	execute func(context.Context, P) (O, error),
	validate func(O) error,
) (readOnlyTool, error) {
	if lo.IsNil(provider) {
		return readOnlyTool{}, missingProvider
	}
	call := func(ctx context.Context, request I) (O, error) {
		prepared, err := prepare(request)
		if err != nil {
			var zero O
			return zero, fmt.Errorf("web: prepare %s request: %w", operation, err)
		}
		response, err := execute(ctx, prepared)
		if err != nil {
			var zero O
			return zero, fmt.Errorf("web: execute %s: %w", operation, err)
		}
		if err := validate(response); err != nil {
			var zero O
			return zero, fmt.Errorf("web: validate %s response: %w", operation, err)
		}
		return response, nil
	}
	bound, err := newReadOnlyTool(config, call)
	if err != nil {
		return readOnlyTool{}, fmt.Errorf("web: build %s tool: %w", operation, err)
	}
	return bound, nil
}

func (r readOnlyTool) Definition() chat.ToolDefinition { return r.inner.Definition() }

func (r readOnlyTool) Call(ctx context.Context, invocation toolcontract.Invocation) (chat.ToolOutput, error) {
	return r.inner.Call(ctx, invocation)
}

// ConcurrencyPolicy declares independent calls to the concurrent-safe backend.
func (readOnlyTool) ConcurrencyPolicy() func(toolcontract.Invocation) (string, bool) {
	return func(toolcontract.Invocation) (string, bool) { return "", true }
}

// Unwrap exposes the typed input contract through tool decorators.
func (r readOnlyTool) Unwrap() toolcontract.Tool { return r.inner }
