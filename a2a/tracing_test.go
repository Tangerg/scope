package a2a

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/Tangerg/scope/core/tool"
)

type failingTraceAgent struct{}

func (f failingTraceAgent) Run(context.Context, string) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		if yield("partial answer", nil) {
			yield("", errors.New("private-agent-error-marker"))
		}
	}
}

func TestRoundTripErrorTelemetryExcludesContent(t *testing.T) {
	recorder := recordTraceSpans(t)

	var delegate http.Handler
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delegate.ServeHTTP(w, r)
	}))
	defer server.Close()
	card := &sdka2a.AgentCard{
		Name: "Failing Agent", Description: "Returns a failure",
		SupportedInterfaces: []*sdka2a.AgentInterface{sdka2a.NewAgentInterface(server.URL+"/invoke", sdka2a.TransportProtocolJSONRPC)},
		DefaultInputModes:   []string{"text/plain"}, DefaultOutputModes: []string{"text/plain"},
		Capabilities: sdka2a.AgentCapabilities{Streaming: true},
		Skills:       []sdka2a.AgentSkill{{ID: "fail", Name: "Fail", Description: "Return a failure", Tags: []string{"fail"}}},
	}
	var err error
	delegate, err = NewHTTPHandler(ServerConfig{Agent: failingTraceAgent{}, Card: card,
		TaskStore: taskstore.NewInMemory(&taskstore.InMemoryStoreConfig{Authenticator: a2asrv.NewTaskStoreAuthenticator()})})
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
	invocation, err := binding.Contract().Prepare(tool.Request{Name: executable.Definition().Name, Arguments: `{"message":"hello"}`}, tool.Admission{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = binding.Call(t.Context(), invocation)
	failure, ok := errors.AsType[*tool.Failure](err)
	if !ok {
		t.Fatalf("caller error = %v, want a definite failure", err)
	}
	if text, _ := failure.Output().Text(); !strings.HasSuffix(text, "\npartial answer") {
		t.Fatalf("failure output = %q, want the partial artifact", text)
	}
	remote, ok := errors.AsType[*RemoteAgentError](failure.Cause())
	if !ok || remote.Detail != "agent execution failed" {
		t.Fatalf("caller error = %v, want the public failure message", err)
	}
	spans := recorder.Ended()
	if len(spans) != 2 {
		t.Fatalf("ended spans = %d, want server and client", len(spans))
	}
	for _, span := range spans {
		want := "*errors.errorString"
		if span.Name() == "a2a.agent.call Failing_Agent" {
			want = "*tool.Failure"
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

func recordTraceSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(recorder))
	previous := a2aTracer
	a2aTracer = provider.Tracer("test")
	t.Cleanup(func() {
		a2aTracer = previous
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return recorder
}

func traceServer(t *testing.T, agent Agent) *httptest.Server {
	t.Helper()
	var delegate http.Handler
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delegate.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	card := &sdka2a.AgentCard{
		Name:                "Trace Agent",
		SupportedInterfaces: []*sdka2a.AgentInterface{sdka2a.NewAgentInterface(server.URL+"/invoke", sdka2a.TransportProtocolJSONRPC)},
		DefaultInputModes:   []string{"text/plain"}, DefaultOutputModes: []string{"text/plain"},
	}
	var err error
	delegate, err = NewHTTPHandler(ServerConfig{Agent: agent, Card: card,
		TaskStore: taskstore.NewInMemory(&taskstore.InMemoryStoreConfig{Authenticator: a2asrv.NewTaskStoreAuthenticator()})})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func traceInvocation(t *testing.T, endpoint Endpoint) (tool.Binding, tool.Invocation) {
	t.Helper()
	set, err := OpenToolSet(t.Context(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := set.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	binding, err := tool.Bind(set.Tools()[0])
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := binding.Contract().Prepare(tool.Request{Name: binding.Contract().Name(), Arguments: `{"message":"hello"}`}, tool.Admission{})
	if err != nil {
		t.Fatal(err)
	}
	return binding, invocation
}

type tracePanicTransport struct {
	base  http.RoundTripper
	value any
}

func (t tracePanicTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method == http.MethodPost {
		panic(t.value)
	}
	return t.base.RoundTrip(request)
}

func TestClientPanicTelemetryPreservesPanicWithoutContent(t *testing.T) {
	recorder := recordTraceSpans(t)
	server := traceServer(t, chunkedAgent{"done"})
	privateDiagnostic := &struct{ message string }{message: "private-transport-panic"}
	client := &http.Client{Transport: tracePanicTransport{base: server.Client().Transport, value: privateDiagnostic}}
	binding, invocation := traceInvocation(t, Endpoint{CardURL: server.URL, HTTPClient: client})
	var panicked any
	func() {
		defer func() { panicked = recover() }()
		_, _ = binding.Call(t.Context(), invocation)
	}()
	if panicked != privateDiagnostic {
		t.Fatalf("panic = %v, want the original diagnostic identity", panicked)
	}
	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want client only", len(spans))
	}
	assertSpanErrorClassification(t, spans[0], "panic")
	if spans[0].Status().Code != codes.Error || spans[0].Status().Description != "panic" {
		t.Errorf("status = %v, want Error/panic", spans[0].Status())
	}
	if content := fmt.Sprint(spans[0].Attributes(), spans[0].Events(), spans[0].Status()); strings.Contains(content, privateDiagnostic.message) {
		t.Errorf("telemetry contains private panic content: %s", content)
	}
}

type tracePanicAgent struct{ value any }

func (t tracePanicAgent) Run(context.Context, string) iter.Seq2[string, error] {
	return func(func(string, error) bool) { panic(t.value) }
}

type privateLogSink struct {
	marker string
	found  atomic.Bool
}

func (p *privateLogSink) Write(data []byte) (int, error) {
	if strings.Contains(string(data), p.marker) {
		p.found.Store(true)
	}
	return len(data), nil
}

func TestServerPanicTelemetryExcludesContent(t *testing.T) {
	recorder := recordTraceSpans(t)
	privateDiagnostic := errors.New("private-agent-panic")
	logs := &privateLogSink{marker: privateDiagnostic.Error()}
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	server := traceServer(t, tracePanicAgent{value: privateDiagnostic})
	binding, invocation := traceInvocation(t, Endpoint{CardURL: server.URL})
	_, err := binding.Call(t.Context(), invocation)
	if err == nil || strings.Contains(err.Error(), privateDiagnostic.Error()) {
		t.Fatalf("remote error = %v, want a content-free protocol error", err)
	}
	if logs.found.Load() {
		t.Error("SDK worker recovery logged private panic content")
	}
	spans := recorder.Ended()
	if len(spans) != 2 {
		t.Fatalf("ended spans = %d, want server and client", len(spans))
	}
	for _, span := range spans {
		if content := fmt.Sprint(span.Attributes(), span.Events(), span.Status()); strings.Contains(content, privateDiagnostic.Error()) {
			t.Errorf("%s telemetry contains private panic content: %s", span.Name(), content)
		}
		if span.Name() == "a2a.agent.serve" {
			assertSpanErrorClassification(t, span, "panic")
			if span.Status().Code != codes.Error || span.Status().Description != "panic" {
				t.Errorf("status = %v, want Error/panic", span.Status())
			}
		}
	}
}

func TestExecutorInputRejectionTelemetry(t *testing.T) {
	recorder := recordTraceSpans(t)
	exec, err := newExecutor(chunkedAgent{"done"})
	if err != nil {
		t.Fatal(err)
	}
	part := sdka2a.NewTextPart("hello")
	part.Metadata = map[string]any{"private": "private-part-metadata"}
	execCtx := &a2asrv.ExecutorContext{
		TaskID: "task", ContextID: "context", Message: sdka2a.NewMessage(sdka2a.MessageRoleUser, part),
	}
	var rejected error
	for _, eventErr := range exec.Execute(t.Context(), execCtx) {
		rejected = eventErr
	}
	if !errors.Is(rejected, ErrUnsupportedContent) || !errors.Is(rejected, sdka2a.ErrUnsupportedContentType) {
		t.Fatalf("error = %v, want unsupported content", rejected)
	}
	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want server only", len(spans))
	}
	assertSpanErrorClassification(t, spans[0], "*fmt.wrapErrors")
}
