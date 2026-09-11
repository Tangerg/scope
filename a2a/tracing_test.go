package a2a

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

type failingTraceAgent struct{}

func (f failingTraceAgent) Run(context.Context, string) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		yield("", errors.New("private-agent-error-marker"))
	}
}

func TestRoundTripErrorTelemetryExcludesContent(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := a2aTracer
	a2aTracer = provider.Tracer("test")
	t.Cleanup(func() {
		a2aTracer = previous
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})

	var delegate http.Handler
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delegate.ServeHTTP(w, r)
	}))
	defer server.Close()
	card := &sdka2a.AgentCard{
		Name: "Failing Agent", Description: "Returns a failure",
		SupportedInterfaces: []*sdka2a.AgentInterface{sdka2a.NewAgentInterface(server.URL+"/invoke", sdka2a.TransportProtocolJSONRPC)},
		DefaultInputModes:   []string{"text"}, DefaultOutputModes: []string{"text"},
		Capabilities: sdka2a.AgentCapabilities{Streaming: true},
		Skills:       []sdka2a.AgentSkill{{ID: "fail", Name: "Fail", Description: "Return a failure", Tags: []string{"fail"}}},
	}
	var err error
	delegate, err = NewHTTPHandler(ServerConfig{Agent: failingTraceAgent{}, Card: card})
	if err != nil {
		t.Fatal(err)
	}
	set, err := OpenToolSet(t.Context(), Endpoint{CardURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := set.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	executable := set.Tools()[0]
	binding, err := tool.Bind(executable)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := binding.Contract().Prepare(corechat.ToolCall{ID: "call", Name: executable.Definition().Name, Arguments: `{"message":"hello"}`})
	if err != nil {
		t.Fatal(err)
	}
	_, err = binding.Call(t.Context(), invocation)
	remote, ok := errors.AsType[*RemoteAgentError](err)
	if !ok || remote.Detail != "private-agent-error-marker" {
		t.Fatalf("caller error = %v, want original remote detail", err)
	}
	spans := recorder.Ended()
	if len(spans) != 2 {
		t.Fatalf("ended spans = %d, want server and client", len(spans))
	}
	for _, span := range spans {
		want := "*errors.errorString"
		if span.Name() == "a2a.agent.call failing_agent" {
			want = "*a2a.RemoteAgentError"
		}
		assertSpanErrorClassification(t, span, want)

		if span.Status().Code != codes.Error || span.Status().Description != want {
			t.Errorf("%s status = %v, want Error/%s", span.Name(), span.Status(), want)
		}
		if content := fmt.Sprint(span.Attributes(), span.Events(), span.Status()); strings.Contains(content, "private-agent-error-marker") {
			t.Errorf("%s telemetry contains private error content: %s", span.Name(), content)
		}
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
