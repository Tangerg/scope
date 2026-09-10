package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Tangerg/scope/core/tool"
	scopemcp "github.com/Tangerg/scope/mcp"
)

// startServerWithFailing exposes one tool that always returns IsError=true.
func startServerWithFailing(t *testing.T, ctx context.Context) (*sdkmcp.ClientSession, func()) {
	t.Helper()
	srvT, cliT := sdkmcp.NewInMemoryTransports()

	srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "fail-srv"}, nil)
	srv.AddTool(
		&sdkmcp.Tool{Name: "boom", Description: "always fails", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return &sdkmcp.CallToolResult{
				Content:           []sdkmcp.Content{&sdkmcp.TextContent{Text: "kaboom"}, &sdkmcp.TextContent{Text: "field name is required"}},
				StructuredContent: map[string]any{"field": "name", "code": "required"},
				IsError:           true,
			}, nil
		},
	)
	ss, err := srv.Connect(ctx, srvT, nil)
	require.NoError(t, err)

	cli := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "fail-cli"}, nil)
	cs, err := cli.Connect(ctx, cliT, nil)
	require.NoError(t, err)

	return cs, func() {
		_ = cs.Close()
		_ = ss.Close()
	}
}

func TestTool_IsErrorPreservesCompleteFailure(t *testing.T) {
	ctx := t.Context()
	cs, cleanup := startServerWithFailing(t, ctx)
	defer cleanup()

	tools, err := scopemcp.DiscoverTools(ctx, []scopemcp.ToolSource{{Name: "s", Session: cs}}, scopemcp.ToolDiscoveryConfig{})
	require.NoError(t, err)
	require.Len(t, tools, 1)

	callable := tools[0]
	out, err := invokeTestTool(ctx, callable, "{}")
	require.Error(t, err)
	assert.Empty(t, out)

	// errors.AsType both classifies the error and exposes the structured payload.
	failure, ok := errors.AsType[*tool.Failure](err)
	require.True(t, ok, "expected tool.Failure, got %v", err)
	content := failure.Output()
	require.Len(t, content.Content, 2)
	assert.Equal(t, "kaboom", content.Content[0].Text)
	assert.Equal(t, "field name is required", content.Content[1].Text)
	assert.JSONEq(t, `{"field":"name","code":"required"}`, string(content.Details))
}

func TestTool_RPCErrorIsNotToolFailure(t *testing.T) {
	// Closing the session before a Call forces a transport error,
	// which must not be classified as a known tool failure.
	ctx := t.Context()
	cs, cleanup := startServerWithFailing(t, ctx)
	tools, err := scopemcp.DiscoverTools(ctx, []scopemcp.ToolSource{{Name: "s", Session: cs}}, scopemcp.ToolDiscoveryConfig{})
	require.NoError(t, err)
	require.Len(t, tools, 1)
	cleanup() // close immediately

	_, callErr := invokeTestTool(ctx, tools[0], "{}")
	require.Error(t, callErr)
	_, ok := errors.AsType[*tool.Failure](callErr)
	assert.False(t, ok, "transport errors must not unwrap into tool.Failure")
}

func TestTool_EmptyArgumentsAreValidatedAsEmptyObject(t *testing.T) {
	ctx := t.Context()
	cs, _, cleanup := startServerWithEcho(t, ctx)
	defer cleanup()

	tools, err := scopemcp.DiscoverTools(ctx, []scopemcp.ToolSource{{Name: "s", Session: cs}}, scopemcp.ToolDiscoveryConfig{})
	require.NoError(t, err)

	callable := tools[0]

	// Blank arguments normalize to an empty object and fail the advertised
	// required-property contract before any remote call.
	_, err = invokeTestTool(ctx, callable, "")
	require.Error(t, err)
}

func TestTool_MetaForwardedToServer(t *testing.T) {
	ctx := t.Context()
	srvT, cliT := sdkmcp.NewInMemoryTransports()

	receivedMeta := make(chan map[string]any, 1)
	srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "meta-srv"}, nil)
	srv.AddTool(
		&sdkmcp.Tool{Name: "snitch", Description: "reports meta", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			receivedMeta <- map[string]any(req.Params.Meta)
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "ok"}}}, nil
		},
	)
	ss, err := srv.Connect(ctx, srvT, nil)
	require.NoError(t, err)
	defer ss.Close()

	cli := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "meta-cli"}, nil)
	cs, err := cli.Connect(ctx, cliT, nil)
	require.NoError(t, err)
	defer cs.Close()

	tools, err := scopemcp.DiscoverTools(ctx, []scopemcp.ToolSource{{Name: "src", Session: cs}}, scopemcp.ToolDiscoveryConfig{
		RequestMeta: scopemcp.RequestMetaFromContext,
	})
	require.NoError(t, err)

	callCtx := scopemcp.WithRequestMeta(ctx, sdkmcp.Meta{"userId": "u-42", "trace": "tx-99"})
	out, err := invokeTestTool(callCtx, tools[0], "{}")
	require.NoError(t, err)
	assert.Equal(t, "ok", testOutputText(out))

	got := <-receivedMeta
	assert.Equal(t, "u-42", got["userId"])
	assert.Equal(t, "tx-99", got["trace"])
}
