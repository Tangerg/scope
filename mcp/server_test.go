package mcp_test

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/go-sdk/jsonrpc"
	sdkmcp "github.com/Tangerg/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corecontent "github.com/Tangerg/scope/core/content"
	"github.com/Tangerg/scope/core/tool"
	scopemcp "github.com/Tangerg/scope/mcp"
)

type echoInput struct {
	Text string `json:"text"`
}

func newEchoTool() tool.Tool {
	return testTool{
		definition: tool.Definition{
			Name:        "echo",
			Description: "echo the input",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		},
		call: func(_ context.Context, invocation tool.Invocation) (tool.Output, error) {
			var input echoInput
			if err := jsonv2.Unmarshal(invocation.Arguments(), &input); err != nil {
				return tool.Output{}, err
			}
			return tool.NewTextOutput(input.Text), nil
		},
	}
}

func newConstantTool(name string) tool.Tool {
	return testTool{
		definition: tool.Definition{
			Name:        name,
			Description: "return " + name,
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		call: func(context.Context, tool.Invocation) (tool.Output, error) {
			return tool.NewTextOutput(name), nil
		},
	}
}

type testTool struct {
	definition tool.Definition
	call       func(context.Context, tool.Invocation) (tool.Output, error)
}

func (t testTool) Definition() tool.Definition { return t.definition.Clone() }

func (t testTool) Call(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	output, err := t.call(ctx, invocation)
	return tool.Result{Output: output}, err
}

func connectPair(t *testing.T, ctx context.Context, registered ...tool.Tool) (*sdkmcp.ClientSession, func()) {
	t.Helper()
	srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "scope-srv"}, nil)
	require.NoError(t, registerTestTools(srv, registered...))
	return connectServer(t, ctx, srv)
}

func connectServer(t *testing.T, ctx context.Context, srv *sdkmcp.Server) (*sdkmcp.ClientSession, func()) {
	t.Helper()
	srvT, cliT := sdkmcp.NewInMemoryTransports()

	ss, err := srv.Connect(ctx, srvT, nil)
	require.NoError(t, err)

	cli := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-cli"}, nil)
	cs, err := cli.Connect(ctx, cliT, nil)
	require.NoError(t, err)

	return cs, func() {
		_ = cs.Close()
		_ = ss.Close()
	}
}

func TestRegister_RoundTrip(t *testing.T) {
	ctx := t.Context()
	cs, cleanup := connectPair(t, ctx, newEchoTool())
	defer cleanup()

	list, err := cs.ListTools(ctx, nil)
	require.NoError(t, err)
	require.Len(t, list.Tools, 1)
	assert.Equal(t, "echo", list.Tools[0].Name)
	assert.Equal(t, "echo the input", list.Tools[0].Description)

	schema, ok := list.Tools[0].InputSchema.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "object", schema["type"])

	res, err := cs.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "echo",
		Arguments: map[string]any{"text": "round trip"},
	})
	require.NoError(t, err)
	assert.False(t, res.IsError)
	require.Len(t, res.Content, 1)
	tc, ok := res.Content[0].(*sdkmcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "round trip", tc.Text)
}

func TestRegisterUnknownOutcomeRemainsProtocolError(t *testing.T) {
	ctx := t.Context()

	failing := testTool{
		definition: tool.Definition{
			Name:        "boom",
			Description: "always fails",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		call: func(context.Context, tool.Invocation) (tool.Output, error) {
			return tool.Output{}, errors.New("kaboom from scope tool")
		},
	}

	cs, cleanup := connectPair(t, ctx, failing)
	defer cleanup()

	res, err := cs.CallTool(ctx, &sdkmcp.CallToolParams{Name: "boom", Arguments: map[string]any{}})
	require.Nil(t, res)
	protocolError, found := errors.AsType[*jsonrpc.Error](err)
	require.True(t, found, "call error = %v", err)
	assert.Equal(t, jsonrpc.CodeInternalError, int(protocolError.Code))
	assert.Equal(t, "tool call did not produce a valid result", protocolError.Message)
	assert.Empty(t, protocolError.Data)
}

func TestRegisterInputValidatorDefectStaysInternal(t *testing.T) {
	ctx := t.Context()
	var calls atomic.Int32
	defect := validatingTool{
		testTool: testTool{
			definition: tool.Definition{
				Name: "defective", Description: "validator panics",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			},
			call: func(context.Context, tool.Invocation) (tool.Output, error) {
				calls.Add(1)
				return tool.NewTextOutput("unreachable"), nil
			},
		},
		validator: func([]byte) error { panic("validator defect secret diagnostic") },
	}
	cs, cleanup := connectPair(t, ctx, defect)
	defer cleanup()

	res, err := cs.CallTool(ctx, &sdkmcp.CallToolParams{Name: "defective", Arguments: map[string]any{}})
	require.Nil(t, res)
	protocolError, found := errors.AsType[*jsonrpc.Error](err)
	require.True(t, found, "call error = %v", err)
	assert.Equal(t, jsonrpc.CodeInternalError, int(protocolError.Code))
	assert.NotContains(t, protocolError.Message, "secret diagnostic")
	assert.Empty(t, protocolError.Data)
	assert.EqualValues(t, 0, calls.Load(), "a validator defect must not run the tool")
}

func TestRegisterInvalidArgumentsStayModelVisible(t *testing.T) {
	ctx := t.Context()
	var calls atomic.Int32
	picky := validatingTool{
		testTool: testTool{
			definition: tool.Definition{
				Name: "picky", Description: "rejects some input",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			},
			call: func(context.Context, tool.Invocation) (tool.Output, error) {
				calls.Add(1)
				return tool.NewTextOutput("unreachable"), nil
			},
		},
		validator: func([]byte) error {
			rejection, err := tool.NewInputRejection(tool.InputRejectionConfig{Message: "argument is out of range", Cause: errors.New("private rule detail")})
			if err != nil {
				return err
			}
			return fmt.Errorf("private wrapper: %w", rejection)
		},
	}
	cs, cleanup := connectPair(t, ctx, picky)
	defer cleanup()

	res, err := cs.CallTool(ctx, &sdkmcp.CallToolParams{Name: "picky", Arguments: map[string]any{}})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.True(t, res.IsError, "model-correctable argument errors stay visible")
	require.Len(t, res.Content, 1)
	require.Equal(t, "argument is out of range", res.Content[0].(*sdkmcp.TextContent).Text)
	assert.EqualValues(t, 0, calls.Load())
}

func TestToolFailureSurvivesProtocolRoundTrip(t *testing.T) {
	want := tool.Output{
		Content: []corecontent.Block{{Kind: corecontent.Text, Text: "failed"}, {Kind: corecontent.Text, Text: "one file was committed"}},
		Details: json.RawMessage(`{"files":["one"],"complete":false}`),
	}
	failure, err := tool.NewFailure(tool.FailureConfig{Kind: tool.FailureKindFailed, Cause: errors.New("partial execution"), Output: want})
	require.NoError(t, err)
	executable := testTool{
		definition: tool.Definition{Name: "partial", InputSchema: json.RawMessage(`{"type":"object"}`)},
		call: func(context.Context, tool.Invocation) (tool.Output, error) {
			return tool.Output{}, failure
		},
	}
	session, cleanup := connectPair(t, t.Context(), executable)
	defer cleanup()
	remote, err := scopemcp.DiscoverTools(t.Context(), []scopemcp.ToolSource{{Session: session}}, scopemcp.ToolDiscoveryConfig{})
	require.NoError(t, err)
	require.Len(t, remote, 1)
	_, err = invokeTestTool(t.Context(), remote[0], `{}`)
	received, found := errors.AsType[*tool.Failure](err)
	require.True(t, found, "round-trip error = %v", err)
	assert.Equal(t, want.Content, received.Output().Content)
	assert.JSONEq(t, string(want.Details), string(received.Output().Details))
}

func TestRegister_RejectsNilArgs(t *testing.T) {
	srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "x"}, nil)
	require.ErrorIs(t, registerTestTools(nil, newEchoTool()), scopemcp.ErrNilServer)

	for _, test := range []struct {
		name string
		tool tool.Tool
	}{
		{name: "nil"},
		{name: "typed nil", tool: (*badSchemaTool)(nil)},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := registerTestTools(srv, test.tool)
			require.ErrorIs(t, err, tool.ErrInvalidTool)
		})
	}
}

func TestRegister_RejectsInvalidSchema(t *testing.T) {
	srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "x"}, nil)

	require.Error(t, registerTestTools(srv, badSchemaTool{}))

	err := registerTestTools(srv, missingSchemaTypeTool{})
	require.ErrorContains(t, err, `input schema type must be "object"`)
}

// The SDK panics on non-object schemas; the Core validator must reject them first.
func TestRegister_RefusesEverySchemaShapeTheServerCannotHold(t *testing.T) {
	for _, test := range []struct {
		name   string
		schema string
	}{
		{name: "absent", schema: ""},
		{name: "null", schema: `null`},
		{name: "not an object", schema: `[1,2]`},
		{name: "unparseable", schema: `{not-json`},
		{name: "no declared type", schema: `{}`},
		{name: "declares another type", schema: `{"type":"array"}`},
		{name: "type is not a string", schema: `{"type":123}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "x"}, nil)
			err := registerTestTools(srv, testTool{definition: tool.Definition{
				Name:        "shape",
				InputSchema: json.RawMessage(test.schema),
			}})

			require.ErrorIs(t, err, tool.ErrInvalidDefinition)
		})
	}
}

func TestRegister_RejectsDuplicateBatchAtomically(t *testing.T) {
	srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "x"}, nil)
	err := registerTestTools(srv, newConstantTool("duplicate"), newConstantTool("duplicate"))
	require.ErrorIs(t, err, tool.ErrDuplicateTool)

	require.NoError(t, registerTestTools(srv, newConstantTool("after")))
	client, cleanup := connectServer(t, t.Context(), srv)
	defer cleanup()
	listed, err := client.ListTools(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, listed.Tools, 1)
	assert.Equal(t, "after", listed.Tools[0].Name)
}

func TestRegister_SnapshotsDefinitionOnce(t *testing.T) {
	srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "x"}, nil)
	tool := &definitionOnceTool{}
	require.NoError(t, registerTestTools(srv, tool))

	client, cleanup := connectServer(t, t.Context(), srv)
	defer cleanup()
	result, err := client.CallTool(t.Context(), &sdkmcp.CallToolParams{
		Name:      "stable",
		Arguments: map[string]any{},
	})
	require.NoError(t, err)
	require.False(t, result.IsError)
	assert.EqualValues(t, 1, tool.definitionCalls.Load())
}

type validatingTool struct {
	testTool
	validator func([]byte) error
}

func (v validatingTool) InputValidator() func([]byte) error { return v.validator }

type badSchemaTool struct{}

func (badSchemaTool) Definition() tool.Definition {
	return tool.Definition{Name: "bad", InputSchema: json.RawMessage("{not-json")}
}

func (badSchemaTool) Call(context.Context, tool.Invocation) (tool.Result, error) {
	return tool.Result{}, nil
}

type missingSchemaTypeTool struct{}

func (missingSchemaTypeTool) Definition() tool.Definition {
	return tool.Definition{Name: "missing-schema-type", InputSchema: json.RawMessage(`{}`)}
}

func (missingSchemaTypeTool) Call(context.Context, tool.Invocation) (tool.Result, error) {
	return tool.Result{}, nil
}

type definitionOnceTool struct {
	definitionCalls atomic.Int32
}

func (d *definitionOnceTool) Definition() tool.Definition {
	if d.definitionCalls.Add(1) != 1 {
		panic("tool definition was read after registration")
	}
	return tool.Definition{
		Name:        "stable",
		Description: "stable definition",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}
}

func (*definitionOnceTool) Call(context.Context, tool.Invocation) (tool.Result, error) {
	return tool.Result{Output: tool.NewTextOutput("ok")}, nil
}

func TestDirectRegistrationCannotReplayAContinuationAsANewWrite(t *testing.T) {
	var calls atomic.Int32
	native := testTool{definition: tool.Definition{Name: "write", InputSchema: json.RawMessage(`{"type":"object"}`)}, call: func(context.Context, tool.Invocation) (tool.Output, error) {
		calls.Add(1)
		return tool.NewTextOutput("saved"), nil
	}}
	cs, done := connectPair(t, t.Context(), native)
	defer done()
	out, err := cs.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: "write", Arguments: map[string]any{}, RequestState: "unknown-opaque-state"})
	if err == nil || out != nil || calls.Load() != 0 {
		t.Fatalf("unexpected fresh write: %v %v calls=%d", out, err, calls.Load())
	}
}

func registerTestTools(server *sdkmcp.Server, executables ...tool.Tool) error {
	if server == nil {
		return scopemcp.ErrNilServer
	}
	bindings := make([]tool.Binding, 0, len(executables))
	for _, executable := range executables {
		binding, err := tool.Bind(executable)
		if err != nil {
			return err
		}
		bindings = append(bindings, binding)
	}
	return scopemcp.Register(server, bindings...)
}

func TestRegisterValidatorDiagnosticsNeverBecomeModelFeedback(t *testing.T) {
	secret := errors.New("credential and internal host secret")
	rejection, err := tool.NewInputRejection(tool.InputRejectionConfig{Message: "use an even count", Cause: secret})
	require.NoError(t, err)
	failure, err := tool.NewFailure(tool.FailureConfig{Kind: tool.FailureKindFailed, Cause: secret, Output: tool.NewTextOutput("another invocation's output")})
	require.NoError(t, err)
	for _, row := range []struct {
		name      string
		validator func([]byte) error
	}{
		{"plain diagnostic", func([]byte) error { return secret }},
		{"execution outcome", func([]byte) error { return fmt.Errorf("validator: %w", failure) }},
		{"ambiguous rejection", func([]byte) error { return errors.Join(rejection, secret) }},
		{"panic rejection", func([]byte) error { panic(rejection) }},
		{"panic execution outcome", func([]byte) error { panic(failure) }},
		{"invalid rejection", func([]byte) error { return new(tool.InputRejection) }},
		{"cancellation diagnostic", func([]byte) error { return context.Canceled }},
	} {
		t.Run(row.name, func(t *testing.T) {
			var calls atomic.Int32
			executable := validatingTool{testTool: testTool{definition: tool.Definition{Name: "checked", InputSchema: json.RawMessage(`{"type":"object"}`)}, call: func(context.Context, tool.Invocation) (tool.Output, error) {
				calls.Add(1)
				return tool.NewTextOutput("unexpected"), nil
			}}, validator: row.validator}
			session, cleanup := connectPair(t, t.Context(), executable)
			defer cleanup()
			result, err := session.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: "checked", Arguments: map[string]any{}})
			require.Nil(t, result)
			protocolError, found := errors.AsType[*jsonrpc.Error](err)
			require.True(t, found, "call error = %v", err)
			require.Equal(t, jsonrpc.CodeInternalError, int(protocolError.Code))
			require.Equal(t, "tool input validation failed", protocolError.Message)
			require.Empty(t, protocolError.Data)
			require.Zero(t, calls.Load())
		})
	}
}

type privateValidatorInput struct {
	Value string `json:"value"`
}

func (privateValidatorInput) Validate() error {
	return errors.New("private validator credential")
}

type privateDecoderInput struct {
	Value string `json:"value"`
}

func (*privateDecoderInput) UnmarshalJSON([]byte) error {
	return errors.New("private decoder credential")
}

type publicValidatorInput struct {
	Value string `json:"value"`
}

func (publicValidatorInput) Validate() error {
	rejection, err := tool.NewInputRejection(tool.InputRejectionConfig{Message: "value must select an available option", Cause: errors.New("private validation detail")})
	if err != nil {
		return err
	}
	return rejection
}

func TestRegisterTypedAdmissionPublishesOnlyExplicitCorrections(t *testing.T) {
	var calls atomic.Int32
	validator, err := tool.NewFunc(tool.FuncConfig{Name: "validator"}, func(context.Context, privateValidatorInput) (string, error) {
		calls.Add(1)
		return "unexpected", nil
	})
	require.NoError(t, err)
	decoder, err := tool.NewFunc(tool.FuncConfig{Name: "decoder"}, func(context.Context, privateDecoderInput) (string, error) {
		calls.Add(1)
		return "unexpected", nil
	})
	require.NoError(t, err)
	correctable, err := tool.NewFunc(tool.FuncConfig{Name: "correctable"}, func(context.Context, publicValidatorInput) (string, error) {
		calls.Add(1)
		return "unexpected", nil
	})
	require.NoError(t, err)
	for _, row := range []struct {
		executable tool.Tool
		message    string
	}{
		{executable: validator},
		{executable: decoder, message: "arguments cannot be decoded into the tool's input type"},
		{executable: correctable, message: "value must select an available option"},
	} {
		t.Run(row.executable.Definition().Name, func(t *testing.T) {
			session, cleanup := connectPair(t, t.Context(), row.executable)
			defer cleanup()
			result, err := session.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: row.executable.Definition().Name, Arguments: map[string]any{"value": "bad"}})
			if row.message == "" {
				require.Nil(t, result)
				protocolError, found := errors.AsType[*jsonrpc.Error](err)
				require.True(t, found, "error = %v", err)
				require.Equal(t, "tool input validation failed", protocolError.Message)
				require.Empty(t, protocolError.Data)
			} else {
				require.NoError(t, err)
				require.True(t, result.IsError)
				require.Len(t, result.Content, 1)
				require.Equal(t, row.message, result.Content[0].(*sdkmcp.TextContent).Text)
			}
			require.Zero(t, calls.Load())
		})
	}
}
