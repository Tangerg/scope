package mcp_test

import (
	"context"
	"encoding/json"
	"testing"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Tangerg/scope/core/tool"
	scopemcp "github.com/Tangerg/scope/mcp"
)

func TestToolEffectsRequireAnExplicitHostPolicy(t *testing.T) {
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "effect-policy"}, nil)
	server.AddTool(&sdkmcp.Tool{
		Name: "inspect", InputSchema: json.RawMessage(`{"type":"object"}`),
		Annotations: &sdkmcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, DestructiveHint: new(false)},
	}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{}, nil
	})
	session := connectContentServer(t, server)
	sources := []scopemcp.ToolSource{{Name: "trusted", Session: session}}
	untrusted, err := scopemcp.DiscoverTools(t.Context(), sources, scopemcp.ToolDiscoveryConfig{})
	require.NoError(t, err)
	require.Equal(t, tool.EffectUnsafe, untrusted[0].Definition().Effect)

	calls := 0
	trusted, err := scopemcp.DiscoverTools(t.Context(), sources, scopemcp.ToolDiscoveryConfig{
		EffectPolicy: func(source, name string, annotations sdkmcp.ToolAnnotations) tool.Effect {
			calls++
			require.Equal(t, "trusted", source)
			require.Equal(t, "inspect", name)
			require.True(t, annotations.ReadOnlyHint)
			*annotations.DestructiveHint = true
			return tool.EffectSafe
		},
	})
	require.NoError(t, err)
	binding, err := tool.Bind(trusted[0])
	require.NoError(t, err)
	for range 2 {
		definition := trusted[0].Definition()
		require.Equal(t, tool.EffectSafe, definition.Effect)
		definition.Effect = tool.EffectUnsafe
		require.Equal(t, tool.EffectSafe, binding.Contract().Effect())
		invocation, prepareErr := binding.Contract().Prepare(tool.Request{Name: definition.Name, Arguments: `{}`}, tool.Admission{})
		require.NoError(t, prepareErr)
		_, callErr := binding.Call(t.Context(), invocation)
		require.NoError(t, callErr)
	}
	require.Equal(t, 1, calls)

	_, err = scopemcp.DiscoverTools(t.Context(), sources, scopemcp.ToolDiscoveryConfig{
		EffectPolicy: func(_, _ string, _ sdkmcp.ToolAnnotations) tool.Effect { return tool.Effect("invalid") },
	})
	require.Error(t, err)
}

func TestRegistrationPublishesOnlySupportedEffectGuarantees(t *testing.T) {
	for _, effect := range []tool.Effect{tool.EffectSafe, tool.EffectIdempotent, tool.EffectUnsafe} {
		t.Run(string(effect), func(t *testing.T) {
			executable, err := tool.NewFunc(tool.FuncConfig{Name: "inspect", Effect: effect},
				func(context.Context, tool.IdempotencyKey, struct{}) (string, error) { return "done", nil })
			require.NoError(t, err)
			binding, err := tool.Bind(executable)
			require.NoError(t, err)
			server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "registered"}, nil)
			require.NoError(t, scopemcp.Register(server, binding))
			session := connectContentServer(t, server)
			listed, err := session.ListTools(t.Context(), nil)
			require.NoError(t, err)
			require.Len(t, listed.Tools, 1)
			annotations := listed.Tools[0].Annotations
			if annotations == nil {
				require.NotEqual(t, tool.EffectSafe, effect)
				return
			}
			require.Equal(t, effect == tool.EffectSafe, annotations.ReadOnlyHint)
			require.False(t, annotations.IdempotentHint)
		})
	}
}
