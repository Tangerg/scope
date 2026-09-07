package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

type failingTraceTool struct{}

func (f failingTraceTool) Definition() corechat.ToolDefinition {
	return corechat.ToolDefinition{Name: "fail", Description: "Return a failure", InputSchema: []byte(`{"type":"object"}`)}
}

func (f failingTraceTool) Call(context.Context, tool.Invocation) (corechat.ToolOutput, error) {
	return corechat.ToolOutput{}, errors.New("private-tool-error-marker")
}

func TestRoundTripErrorTelemetryExcludesContent(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := mcpTracer
	mcpTracer = provider.Tracer("test")
	t.Cleanup(func() {
		mcpTracer = previous
		require.NoError(t, provider.Shutdown(context.Background()))
	})

	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "test-server"}, nil)
	require.NoError(t, Register(server, failingTraceTool{}))
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
	binding, err := tool.Bind(executables[0])
	require.NoError(t, err)
	invocation, err := binding.Prepare(corechat.ToolCall{ID: "call", Name: executables[0].Definition().Name, Arguments: `{}`})
	require.NoError(t, err)
	_, err = binding.Call(t.Context(), invocation)
	remote, ok := errors.AsType[*ToolCallError](err)
	require.True(t, ok, "caller error = %v", err)
	require.Equal(t, "private-tool-error-marker", remote.Message)
	spans := recorder.Ended()
	require.Len(t, spans, 2)
	for _, span := range spans {
		want := "*errors.errorString"
		if span.Name() == "mcp.tool.call fail" {
			want = "*mcp.ToolCallError"
		}
		assertSpanErrorClassification(t, span, want)

		require.Equal(t, codes.Error, span.Status().Code)
		require.Equal(t, want, span.Status().Description)
		require.NotContains(t, fmt.Sprint(span.Attributes(), span.Events(), span.Status()), "private-tool-error-marker")
	}
}

func TestErrorTelemetryClassifiesWrappedFailures(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{name: "cancellation", err: context.Canceled, want: "context.canceled"},
		{name: "deadline", err: context.DeadlineExceeded, want: "context.deadline_exceeded"},
		{name: "ordinary error", err: errors.New("private-message"), want: "*errors.errorString"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			defer func() {
				if err := provider.Shutdown(context.Background()); err != nil {
					t.Error(err)
				}
			}()
			_, span := provider.Tracer("test").Start(t.Context(), "failure")
			recordSpanError(span, fmt.Errorf("private-wrapper: %w", test.err))
			span.End()
			recorded := recorder.Ended()[0]
			assertSpanErrorClassification(t, recorded, test.want)
			if recorded.Status().Code != codes.Error || recorded.Status().Description != test.want {
				t.Errorf("status = %v, want Error/%s", recorded.Status(), test.want)
			}
			if strings.Contains(fmt.Sprint(recorded.Attributes(), recorded.Events(), recorded.Status()), "private") {
				t.Fatal("telemetry contains raw error content")
			}
		})
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
