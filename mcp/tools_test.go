package mcp_test

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"sync/atomic"
	"testing"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	toolcontract "github.com/Tangerg/scope/core/tool"
	scopemcp "github.com/Tangerg/scope/mcp"
)

const echoSchema = `{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`

type concurrencyDeclarer interface {
	ConcurrencyPolicy() func(toolcontract.Invocation) (key string, concurrent bool)
}

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
			if err := jsonv2.Unmarshal(req.Params.Arguments, &p); err != nil {
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

func TestToolsDefaultNamingPreservesRemoteIdentity(t *testing.T) {
	cases := []struct {
		source string
		tool   string
		want   string
	}{
		{"html.to.design", "import-url", "html.to.design/import-url"},
		{"srv", "ok_tool-1", "srv/ok_tool-1"},
		{"", "bare.tool", "bare.tool"},
		{"a b", "c/d", "a%20b/c%2Fd"},
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
		assert.NoError(t, tools[0].Definition().Validate())
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
	assert.Equal(t, "primary/echo", def.Name)
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
	assert.ElementsMatch(t, []string{"alpha/echo", "beta/echo"}, names)
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
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "policy-test"}, nil)
	server.AddTool(&sdkmcp.Tool{
		Name: "echo", Description: "echo the input", InputSchema: json.RawMessage(echoSchema),
		Annotations: &sdkmcp.ToolAnnotations{DestructiveHint: new(false), OpenWorldHint: new(false)},
	}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "stable"}}}, nil
	})
	cs := connectContentServer(t, server)
	var destructive, openWorld []bool
	wrapped, err := scopemcp.DiscoverTools(ctx, []scopemcp.ToolSource{{Name: "primary", Session: cs}}, scopemcp.ToolDiscoveryConfig{
		ConcurrencyPolicy: func(_, _ string, annotations sdkmcp.ToolAnnotations, _ toolcontract.Invocation) (string, bool) {
			require.NotNil(t, annotations.DestructiveHint)
			require.NotNil(t, annotations.OpenWorldHint)
			destructive = append(destructive, *annotations.DestructiveHint)
			openWorld = append(openWorld, *annotations.OpenWorldHint)
			*annotations.DestructiveHint = true
			*annotations.OpenWorldHint = true
			return "", true
		},
	})
	require.NoError(t, err)
	require.Len(t, wrapped, 1)

	definition := wrapped[0].Definition()
	assert.Equal(t, "primary/echo", definition.Name)
	assert.Equal(t, "echo the input", definition.Description)

	scheduled := wrapped[0].(concurrencyDeclarer)
	policy := scheduled.ConcurrencyPolicy()
	_, invocation, err := prepareTestTool(wrapped[0], `{"text":"stable"}`)
	require.NoError(t, err)
	for range 2 {
		_, concurrent := policy(invocation)
		assert.True(t, concurrent)
	}
	_, concurrent := scheduled.ConcurrencyPolicy()(invocation)
	assert.True(t, concurrent)
	assert.Equal(t, []bool{false, false, false}, destructive)
	assert.Equal(t, []bool{false, false, false}, openWorld)

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
	require.ErrorIs(t, err, toolcontract.ErrInvalidDefinition)
}

func TestToolsRejectsInvalidPublicName(t *testing.T) {
	ctx := t.Context()
	cs, _, cleanup := startServerWithEcho(t, ctx)
	defer cleanup()

	_, err := scopemcp.DiscoverTools(ctx, []scopemcp.ToolSource{{Name: "src", Session: cs}}, scopemcp.ToolDiscoveryConfig{
		PublicName: func(string, string) string { return "invalid name" },
	})
	require.ErrorIs(t, err, toolcontract.ErrInvalidDefinition)
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
	assert.ElementsMatch(t, []string{"p/echo", "p/ping"}, names)
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
	assert.Equal(t, "primary/echo", tools[0].Definition().Name, "default naming should join source and tool")
}

func TestIncompleteRemoteToolResultIsNotACompletion(t *testing.T) {
	serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "pending-server"}, nil)
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "manual-client"}, &sdkmcp.ClientOptions{MultiRoundTrip: &sdkmcp.MultiRoundTripOptions{Disabled: true}})
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	require.NoError(t, err)
	defer serverSession.Close()
	session, err := client.Connect(t.Context(), clientTransport, nil)
	require.NoError(t, err)
	defer session.Close()
	server.AddTool(&sdkmcp.Tool{Name: "pending", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{InputRequests: sdkmcp.InputRequestMap{}, RequestState: "pending"}, nil
	})
	server.AddTool(&sdkmcp.Tool{Name: "outside", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{RequestState: "pending", InputRequests: sdkmcp.InputRequestMap{"form": &sdkmcp.ElicitParams{Mode: "url", Message: "External sign-in", URL: "https://example.invalid/authorize", ElicitationID: "foreign"}}}, nil
	})
	server.AddTool(&sdkmcp.Tool{Name: "compound", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{RequestState: "pending", InputRequests: sdkmcp.InputRequestMap{"form": &sdkmcp.ElicitParams{Mode: "form", Message: "Choose", RequestedSchema: json.RawMessage(`{"type":"object","properties":{"value":{"$ref":"#/$defs/number"}},"$defs":{"number":{"type":"integer"}}}`)}}}, nil
	})
	tools, err := scopemcp.DiscoverTools(t.Context(), []scopemcp.ToolSource{{Session: session}}, scopemcp.ToolDiscoveryConfig{})
	require.NoError(t, err)
	require.Len(t, tools, 3)
	for _, candidate := range tools {
		output, callErr := invokeTestTool(t.Context(), candidate, `{}`)
		require.ErrorIs(t, callErr, scopemcp.ErrIncompleteResult)
		assert.Empty(t, output.Content)
	}

}

func TestFormAnswerResumesOnlyOriginalBinding(t *testing.T) {
	st, ct := sdkmcp.NewInMemoryTransports()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "form"}, nil)
	var calls atomic.Int32
	server.AddTool(&sdkmcp.Tool{Name: "book", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		calls.Add(1)
		if req.Params.RequestState == "" {
			return &sdkmcp.CallToolResult{RequestState: "reservation-1", InputRequests: sdkmcp.InputRequestMap{"city": &sdkmcp.ElicitParams{Mode: "form", Message: "Destination?", RequestedSchema: json.RawMessage(`{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}}`)}}}, nil
		}
		answer, ok := req.Params.InputResponses["city"].(*sdkmcp.ElicitResult)
		if !ok || answer.Action != "accept" || answer.Content["name"] != "Paris" || req.Params.RequestState != "reservation-1" {
			return nil, errors.New("misdirected continuation")
		}
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "confirmed"}}}, nil
	})
	ss, err := server.Connect(t.Context(), st, nil)
	require.NoError(t, err)
	defer ss.Close()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "host"}, &sdkmcp.ClientOptions{MultiRoundTrip: &sdkmcp.MultiRoundTripOptions{Disabled: true}})
	cs, err := client.Connect(t.Context(), ct, nil)
	require.NoError(t, err)
	defer cs.Close()
	discover := func() toolcontract.Tool {
		ts, discoverToolsErr := scopemcp.DiscoverTools(t.Context(), []scopemcp.ToolSource{{Session: cs}}, scopemcp.ToolDiscoveryConfig{})
		require.NoError(t, discoverToolsErr)
		return ts[0]
	}
	b, inv, err := prepareTestTool(discover(), "{}")
	require.NoError(t, err)
	pending, err := b.Call(t.Context(), inv)
	require.NoError(t, err)
	require.NotNil(t, pending.InputRequest)
	for _, answer := range []string{
		`{"action":"decline"}`,
		`{"action":"cancel"}`,
		`{"action":"accept","content":{"name":"Paris"}}`,
	} {
		_, answerErr := pending.InputRequest.Continuation(json.RawMessage(answer))
		require.NoError(t, answerErr, answer)
	}
	for _, answer := range []string{
		`{"action":"accept","content":{}}`,
		`{"action":"accept"}`,
		`{"action":"accept","content":{"name":false}}`,
		`{"action":"decline","content":{}}`,
		`{"action":"cancel","content":{}}`,
		`{"action":"other"}`,
		`{"action":"accept","content":{"name":"Paris"},"extra":true}`,
	} {
		_, answerErr := pending.InputRequest.Continuation(json.RawMessage(answer))
		require.Error(t, answerErr, answer)
	}
	c, err := pending.InputRequest.Continuation(json.RawMessage(`{"action":"accept","content":{"name":"Paris"}}`))
	require.NoError(t, err)
	call := toolcontract.Request{Name: "book", Arguments: "{}"}
	other, _, err := prepareTestTool(discover(), "{}")
	require.NoError(t, err)
	retry, err := other.Contract().Prepare(call, toolcontract.Admission{Continuation: &c})
	require.NoError(t, err)
	_, err = other.Call(t.Context(), retry)
	require.ErrorIs(t, err, scopemcp.ErrContinuationBinding)
	require.EqualValues(t, 1, calls.Load())
	retry, err = b.Contract().Prepare(call, toolcontract.Admission{Continuation: &c})
	require.NoError(t, err)
	result, err := b.Call(t.Context(), retry)
	require.NoError(t, err)
	require.Equal(t, "confirmed", testOutputText(result.Output))
	require.EqualValues(t, 2, calls.Load())
}
