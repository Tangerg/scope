package mcp

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Tangerg/go-sdk/jsonrpc"
	sdkmcp "github.com/Tangerg/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	corecontent "github.com/Tangerg/scope/core/content"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/tool"
)

type failingTraceTool struct{}

func (f failingTraceTool) Definition() tool.Definition {
	return tool.Definition{Name: "fail", Description: "Return a failure", InputSchema: []byte(`{"type":"object"}`)}
}

func (f failingTraceTool) Call(context.Context, tool.Invocation) (tool.Result, error) {
	return tool.Result{}, errors.New("private-tool-error-marker")
}

func TestRoundTripErrorTelemetryExcludesContent(t *testing.T) {
	recorder := recordTraceSpans(t)

	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "test-server"}, nil)
	binding, err := tool.Bind(failingTraceTool{})
	require.NoError(t, err)
	require.NoError(t, Register(server, binding))
	serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, serverSession.Close()) }()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client"}, nil)
	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, clientSession.Close()) }()
	executables, err := DiscoverTools(t.Context(), []ToolSource{{Name: "test", Session: clientSession}}, ToolDiscoveryConfig{})
	require.NoError(t, err)
	require.Len(t, executables, 1)
	binding, err = tool.Bind(executables[0])
	require.NoError(t, err)
	invocation, err := binding.Contract().Prepare(tool.Request{Name: executables[0].Definition().Name, Arguments: `{}`}, tool.Admission{})
	require.NoError(t, err)
	_, err = binding.Call(t.Context(), invocation)
	_, ok := errors.AsType[*tool.Failure](err)
	require.False(t, ok, "unknown outcome became definite: %v", err)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "private-tool-error-marker")
	spans := recorder.Ended()
	require.Len(t, spans, 2)
	for _, span := range spans {
		want := "*errors.errorString"
		if span.Name() == "mcp.tool.call fail" {
			want = "*jsonrpc2.WireError"
		}
		assertSpanErrorClassification(t, span, want)

		require.Equal(t, codes.Error, span.Status().Code)
		require.Equal(t, want, span.Status().Description)
		require.NotContains(t, fmt.Sprint(span.Attributes(), span.Events(), span.Status()), "private-tool-error-marker")
	}
}

func assertSpanErrorClassification(t *testing.T, span sdktrace.ReadOnlySpan, want string) {
	t.Helper()
	found := false
	for _, attr := range span.Attributes() {
		if attr.Key == "error.type" {
			found = true
			if attr.Value.AsString() != want {
				t.Errorf("error.type = %s, want %s", attr.Value.AsString(), want)
			}
		}
	}
	if !found {
		t.Error("span is missing error.type")
	}
	events := span.Events()
	if len(events) != 1 || events[0].Name != "exception" || len(events[0].Attributes) != 1 {
		t.Fatalf("events = %v, want one exception event with only a type", events)
	}
	attr := events[0].Attributes[0]
	if attr.Key != "exception.type" || attr.Value.AsString() != want {
		t.Errorf("exception attribute = %v, want exception.type=%s", attr, want)
	}
}

type traceTool struct {
	call func() (tool.Result, error)
}

func (traceTool) Definition() tool.Definition {
	return tool.Definition{Name: "trace", InputSchema: []byte(`{"type":"object"}`)}
}

func (t traceTool) Call(context.Context, tool.Invocation) (tool.Result, error) { return t.call() }

func traceSession(t *testing.T, executable tool.Tool) *sdkmcp.ClientSession {
	t.Helper()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "trace-server"}, nil)
	binding, err := tool.Bind(executable)
	require.NoError(t, err)
	require.NoError(t, Register(server, binding))
	serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, serverSession.Close()) })
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "trace-client"}, nil)
	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, clientSession.Close()) })
	return clientSession
}

func recordTraceSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(recorder))
	previous := mcpTracer
	mcpTracer = provider.Tracer("test")
	t.Cleanup(func() {
		mcpTracer = previous
		require.NoError(t, provider.Shutdown(context.Background()))
	})
	return recorder
}

func TestClientPanicTelemetryPreservesPanicWithoutContent(t *testing.T) {
	recorder := recordTraceSpans(t)
	session := traceSession(t, traceTool{call: func() (tool.Result, error) { return tool.Result{}, nil }})
	privateDiagnostic := errors.New("private-request-meta-panic")
	executables, err := DiscoverTools(t.Context(), []ToolSource{{Session: session}}, ToolDiscoveryConfig{
		RequestMeta: func(context.Context, tool.Invocation) sdkmcp.Meta { panic(privateDiagnostic) },
	})
	require.NoError(t, err)
	require.Len(t, executables, 1)
	binding, err := tool.Bind(executables[0])
	require.NoError(t, err)
	invocation, err := binding.Contract().Prepare(tool.Request{Name: binding.Contract().Name(), Arguments: `{}`}, tool.Admission{})
	require.NoError(t, err)
	var panicked any
	func() {
		defer func() { panicked = recover() }()
		_, _ = binding.Call(t.Context(), invocation)
	}()
	require.Same(t, privateDiagnostic, panicked)
	spans := recorder.Ended()
	require.Len(t, spans, 1)
	assertSpanErrorClassification(t, spans[0], "panic")
	require.Equal(t, codes.Error, spans[0].Status().Code)
	require.Equal(t, "panic", spans[0].Status().Description)
	require.NotContains(t, fmt.Sprint(spans[0].Attributes(), spans[0].Events(), spans[0].Status()), privateDiagnostic.Error())
}

func TestServerTelemetryRecordsOneOutcome(t *testing.T) {
	const privateDiagnostic = "private-server-boundary-diagnostic"
	reference, err := media.NewReference("image/png", "provider-image")
	require.NoError(t, err)
	failure, err := tool.NewFailure(tool.FailureConfig{
		Kind: tool.FailureKindFailed, Cause: errors.New(privateDiagnostic),
		Output: tool.Output{Content: []corecontent.Block{{Kind: corecontent.Media, Media: reference}}},
	})
	require.NoError(t, err)
	for _, test := range []struct {
		name           string
		state          string
		call           func() (tool.Result, error)
		classification string
		code           int64
	}{
		{name: "unsolicited continuation", state: "unissued-state", call: func() (tool.Result, error) {
			return tool.Result{}, nil
		}, classification: "*jsonrpc2.WireError", code: jsonrpc.CodeInvalidParams},
		{name: "callback panic", call: func() (tool.Result, error) {
			panic(errors.New(privateDiagnostic))
		}, classification: "panic", code: jsonrpc.CodeInternalError},
		{name: "unrepresentable failure output", call: func() (tool.Result, error) {
			return tool.Result{}, failure
		}, classification: "*tool.Failure", code: jsonrpc.CodeInternalError},
		{name: "success", call: func() (tool.Result, error) {
			return tool.Result{Output: tool.NewTextOutput("done")}, nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := recordTraceSpans(t)
			session := traceSession(t, traceTool{call: test.call})
			result, callErr := session.CallTool(t.Context(), &sdkmcp.CallToolParams{
				Name: "trace", Arguments: map[string]any{}, RequestState: test.state,
			})
			spans := recorder.Ended()
			require.Len(t, spans, 1)
			if test.classification == "" {
				require.NoError(t, callErr)
				require.NotNil(t, result)
				require.Empty(t, spans[0].Events())
				require.NotEqual(t, codes.Error, spans[0].Status().Code)
				return
			}
			protocolError, found := errors.AsType[*jsonrpc.Error](callErr)
			require.True(t, found, "error = %v", callErr)
			require.Equal(t, test.code, protocolError.Code)
			require.Nil(t, result)
			require.NotContains(t, callErr.Error(), privateDiagnostic)
			assertSpanErrorClassification(t, spans[0], test.classification)
			require.Equal(t, codes.Error, spans[0].Status().Code)
			require.Equal(t, test.classification, spans[0].Status().Description)
			require.NotContains(t, fmt.Sprint(spans[0].Attributes(), spans[0].Events(), spans[0].Status()), privateDiagnostic)
		})
	}
}
