package mcp_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	corechat "github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
	scopemcp "github.com/Tangerg/scope/mcp"
)

const echoSchema = `{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`

type concurrencyDeclarer interface {
	ConcurrencyPolicy() func(toolcontract.Invocation) (key string, concurrent bool)
}

// startServerWithEcho boots an in-memory MCP server that exposes a single
// "echo" tool (text -> text). It returns the live ClientSession the test
// should use to list tools, the underlying Server (so tests can mutate its
// tool list), and a cleanup that closes both sessions.
func startServerWithEcho(t *testing.T, ctx context.Context) (*sdkmcp.ClientSession, *sdkmcp.Server, func()) {
	t.Helper()
	srvT, cliT := sdkmcp.NewInMemoryTransports()

	srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "test-server"}, nil)
	srv.AddTool(
		&sdkmcp.Tool{
			Name:        "echo",
			Description: "echo the input",
			InputSchema: json.RawMessage(echoSchema),
		},
		func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			var p struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(req.Params.Arguments, &p); err != nil {
				return &sdkmcp.CallToolResult{
					Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: err.Error()}},
					IsError: true,
				}, nil
			}
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: p.Text}},
			}, nil
		},
	)

	ss, err := srv.Connect(ctx, srvT, nil)
	require.NoError(t, err)

	cli := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client"}, nil)
	cs, err := cli.Connect(ctx, cliT, nil)
	require.NoError(t, err)

	cleanup := func() {
		_ = cs.Close()
		_ = ss.Close()
	}
	return cs, srv, cleanup
}

// TestToolsDefaultNamingSanitizesForProviderCharset locks the bridge that maps an
// MCP server/tool name onto the provider-accepted function-name charset
// (^[a-zA-Z0-9_-]{1,64}$). A server like "html.to.design" must NOT yield a
// dotted public name — that makes the whole chat request invalid and the
// provider rejects every turn.
func TestToolsDefaultNamingSanitizesForProviderCharset(t *testing.T) {
	cases := []struct {
		source string
		tool   string
		want   string
	}{
		{"html.to.design", "import-url", "html_to_design_import-url"},
		{"srv", "ok_tool-1", "srv_ok_tool-1"}, // already valid → unchanged
		{"", "bare.tool", "bare_tool"},        // empty source → sanitized bare name
		{"a b", "c/d", "a_b_c_d"},             // spaces + slash → underscores
	}
	for _, c := range cases {
		srvT, cliT := sdkmcp.NewInMemoryTransports()
		srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "test-server"}, nil)
		srv.AddTool(&sdkmcp.Tool{Name: c.tool, InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return &sdkmcp.CallToolResult{}, nil
		})
		ss, err := srv.Connect(t.Context(), srvT, nil)
		require.NoError(t, err)
		defer ss.Close()

		cli := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client"}, nil)
		cs, err := cli.Connect(t.Context(), cliT, nil)
		require.NoError(t, err)
		defer cs.Close()

		tools, err := scopemcp.DiscoverTools(t.Context(), []scopemcp.ToolSource{{Name: c.source, Session: cs}}, scopemcp.ToolDiscoveryConfig{})
		require.NoError(t, err)
		require.Len(t, tools, 1)

		got := tools[0].Definition().Name
		assert.Equal(t, c.want, got)
		assert.Regexp(t, `^[a-zA-Z0-9_-]{1,64}$`, got)
	}
}

func TestToolsDiscoversAndCallsTool(t *testing.T) {
	ctx := t.Context()
	cs, _, cleanup := startServerWithEcho(t, ctx)
	defer cleanup()

	tools, err := scopemcp.DiscoverTools(ctx, []scopemcp.ToolSource{{Name: "primary", Session: cs}}, scopemcp.ToolDiscoveryConfig{})
	require.NoError(t, err)
	require.Len(t, tools, 1)

	def := tools[0].Definition()
	assert.Equal(t, "primary_echo", def.Name)
	assert.Equal(t, "echo the input", def.Description)
	assert.NotEmpty(t, def.InputSchema)
	def.InputSchema[0] = '['
	assert.Equal(t, byte('{'), tools[0].Definition().InputSchema[0])

	callable := tools[0]
	out, err := invokeTestTool(ctx, callable, `{"text":"hello world"}`)
	require.NoError(t, err)
	assert.Equal(t, "hello world", testOutputText(out))
}

func TestToolsTwoSourcesAreNamespaced(t *testing.T) {
	ctx := t.Context()
	cs1, _, c1 := startServerWithEcho(t, ctx)
	defer c1()
	cs2, _, c2 := startServerWithEcho(t, ctx)
	defer c2()

	tools, err := scopemcp.DiscoverTools(ctx, []scopemcp.ToolSource{
		{Name: "alpha", Session: cs1},
		{Name: "beta", Session: cs2},
	}, scopemcp.ToolDiscoveryConfig{})
	require.NoError(t, err)
	require.Len(t, tools, 2)

	names := []string{tools[0].Definition().Name, tools[1].Definition().Name}
	assert.ElementsMatch(t, []string{"alpha_echo", "beta_echo"}, names)
}

func TestToolsFailsOnDuplicateNames(t *testing.T) {
	ctx := t.Context()
	cs1, _, c1 := startServerWithEcho(t, ctx)
	defer c1()
	cs2, _, c2 := startServerWithEcho(t, ctx)
	defer c2()

	_, err := scopemcp.DiscoverTools(ctx, []scopemcp.ToolSource{
		{Name: "samename", Session: cs1},
		{Name: "samename", Session: cs2},
	}, scopemcp.ToolDiscoveryConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate tool name")
}

func TestToolsCustomNaming(t *testing.T) {
	ctx := t.Context()
	cs, _, cleanup := startServerWithEcho(t, ctx)
	defer cleanup()

	tools, err := scopemcp.DiscoverTools(ctx, []scopemcp.ToolSource{{Name: "src", Session: cs}}, scopemcp.ToolDiscoveryConfig{
		PublicName: func(_, toolName string) string {
			return "mcp__" + toolName
		},
	})
	require.NoError(t, err)
	require.Len(t, tools, 1)
	assert.Equal(t, "mcp__echo", tools[0].Definition().Name)
}

func TestToolsConcurrencyPolicyReceivesRemoteIdentity(t *testing.T) {
	ctx := t.Context()
	cs, _, cleanup := startServerWithEcho(t, ctx)
	defer cleanup()

	var gotSource, gotTool, gotArguments string
	wrapped, err := scopemcp.DiscoverTools(ctx, []scopemcp.ToolSource{{Name: "primary", Session: cs}}, scopemcp.ToolDiscoveryConfig{
		ConcurrencyPolicy: func(sourceName, toolName string, annotations sdkmcp.ToolAnnotations, invocation toolcontract.Invocation) (string, bool) {
			gotSource = sourceName
			gotTool = toolName
			gotArguments = string(invocation.Arguments())
			return "tenant:acme", true
		},
	})
	require.NoError(t, err)
	require.Len(t, wrapped, 1)

	scheduled, ok := wrapped[0].(concurrencyDeclarer)
	require.True(t, ok)
	_, invocation, err := prepareTestTool(wrapped[0], `{"text":"acme"}`)
	require.NoError(t, err)
	key, concurrent := scheduled.ConcurrencyPolicy()(invocation)
	assert.True(t, concurrent)
	assert.Equal(t, "tenant:acme", key)
	assert.Equal(t, "primary", gotSource)
	assert.Equal(t, "echo", gotTool)
	assert.Equal(t, `{"text":"acme"}`, gotArguments)
}

func TestToolsConcurrencyPolicyCannotMutateDescriptor(t *testing.T) {
	ctx := t.Context()
	cs, _, cleanup := startServerWithEcho(t, ctx)
	defer cleanup()

	var destructive []bool
	wrapped, err := scopemcp.DiscoverTools(ctx, []scopemcp.ToolSource{{Name: "primary", Session: cs}}, scopemcp.ToolDiscoveryConfig{
		ConcurrencyPolicy: func(_, _ string, annotations sdkmcp.ToolAnnotations, _ toolcontract.Invocation) (string, bool) {
			destructive = append(destructive, annotations.DestructiveHint != nil && *annotations.DestructiveHint)
			annotations.DestructiveHint = new(true)
			return "", true
		},
	})
	require.NoError(t, err)
	require.Len(t, wrapped, 1)

	definition := wrapped[0].Definition()
	assert.Equal(t, "primary_echo", definition.Name)
	assert.Equal(t, "echo the input", definition.Description)

	scheduled := wrapped[0].(concurrencyDeclarer)
	policy := scheduled.ConcurrencyPolicy()
	_, invocation, err := prepareTestTool(wrapped[0], `{"text":"stable"}`)
	require.NoError(t, err)
	for range 2 {
		_, concurrent := policy(invocation)
		assert.True(t, concurrent)
	}
	assert.Equal(t, []bool{false, false}, destructive)

	out, err := invokeTestTool(ctx, wrapped[0], `{"text":"stable"}`)
	require.NoError(t, err)
	assert.Equal(t, "stable", testOutputText(out))
}

func TestToolsDefaultConcurrencyIsExclusive(t *testing.T) {
	ctx := t.Context()
	cs, _, cleanup := startServerWithEcho(t, ctx)
	defer cleanup()

	wrapped, err := scopemcp.DiscoverTools(ctx, []scopemcp.ToolSource{{Name: "primary", Session: cs}}, scopemcp.ToolDiscoveryConfig{})
	require.NoError(t, err)
	require.Len(t, wrapped, 1)

	scheduled, ok := wrapped[0].(concurrencyDeclarer)
	require.True(t, ok)
	assert.Nil(t, scheduled.ConcurrencyPolicy())
}

func TestToolsRejectsEmptyPublicName(t *testing.T) {
	ctx := t.Context()
	cs, _, cleanup := startServerWithEcho(t, ctx)
	defer cleanup()

	_, err := scopemcp.DiscoverTools(ctx, []scopemcp.ToolSource{{Name: "src", Session: cs}}, scopemcp.ToolDiscoveryConfig{
		PublicName: func(string, string) string { return "" },
	})
	require.ErrorIs(t, err, corechat.ErrInvalidToolDefinition)
}

func TestToolsRejectsInvalidPublicName(t *testing.T) {
	ctx := t.Context()
	cs, _, cleanup := startServerWithEcho(t, ctx)
	defer cleanup()

	_, err := scopemcp.DiscoverTools(ctx, []scopemcp.ToolSource{{Name: "src", Session: cs}}, scopemcp.ToolDiscoveryConfig{
		PublicName: func(string, string) string { return "invalid name" },
	})
	require.ErrorIs(t, err, corechat.ErrInvalidToolDefinition)
}

func TestToolsReadsCurrentRemoteList(t *testing.T) {
	ctx := t.Context()
	cs, srv, cleanup := startServerWithEcho(t, ctx)
	defer cleanup()

	first, err := scopemcp.DiscoverTools(ctx, []scopemcp.ToolSource{{Name: "p", Session: cs}}, scopemcp.ToolDiscoveryConfig{})
	require.NoError(t, err)
	require.Len(t, first, 1)

	srv.AddTool(
		&sdkmcp.Tool{Name: "ping", Description: "ping", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "pong"}}}, nil
		},
	)

	refreshed, err := scopemcp.DiscoverTools(ctx, []scopemcp.ToolSource{{Name: "p", Session: cs}}, scopemcp.ToolDiscoveryConfig{})
	require.NoError(t, err)
	require.Len(t, refreshed, 2)

	names := []string{refreshed[0].Definition().Name, refreshed[1].Definition().Name}
	assert.ElementsMatch(t, []string{"p_echo", "p_ping"}, names)
}

func TestToolsRejectsNilSession(t *testing.T) {
	_, err := scopemcp.DiscoverTools(t.Context(), []scopemcp.ToolSource{{Name: "x"}}, scopemcp.ToolDiscoveryConfig{})
	require.Error(t, err)
}

func TestToolsZeroOptionsUsesDefaults(t *testing.T) {
	ctx := t.Context()
	cs, _, cleanup := startServerWithEcho(t, ctx)
	defer cleanup()

	tools, err := scopemcp.DiscoverTools(ctx, []scopemcp.ToolSource{{Name: "primary", Session: cs}}, scopemcp.ToolDiscoveryConfig{})
	require.NoError(t, err)
	require.Len(t, tools, 1)
	assert.Equal(t, "primary_echo", tools[0].Definition().Name, "default naming should join source and tool")
}
