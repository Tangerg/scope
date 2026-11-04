package a2a

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
)

type failingTaskStore struct {
	taskstore.Store
	err error
}

type diagnosticRequestHandler struct {
	a2asrv.RequestHandler
	calls *atomic.Int32
	err   error
}

func (d diagnosticRequestHandler) GetTask(context.Context, *sdka2a.GetTaskRequest) (*sdka2a.Task, error) {
	d.calls.Add(1)
	return nil, d.err
}

func (d diagnosticRequestHandler) SubscribeToTask(context.Context, *sdka2a.SubscribeToTaskRequest) iter.Seq2[sdka2a.Event, error] {
	return func(yield func(sdka2a.Event, error) bool) {
		d.calls.Add(1)
		yield(nil, d.err)
	}
}

func TestProtocolHandlerAppliesVersionAndErrorBoundariesToCustomHandlers(t *testing.T) {
	for _, test := range []struct {
		name, method, version string
		calls                 int32
		code                  int
	}{
		{name: "unary diagnostic", method: "GetTask", version: string(sdka2a.Version), calls: 1, code: -32603},
		{name: "stream diagnostic", method: "SubscribeToTask", version: string(sdka2a.Version), calls: 1, code: -32603},
		{name: "missing version", method: "GetTask", code: -32009},
		{name: "old version", method: "GetTask", version: "0.3", code: -32009},
		{name: "stream missing version", method: "SubscribeToTask", code: -32009},
	} {
		t.Run(test.name, func(t *testing.T) {
			const diagnostic = "private-handler-diagnostic"
			var calls atomic.Int32
			card := &sdka2a.AgentCard{Name: "test", SupportedInterfaces: []*sdka2a.AgentInterface{sdka2a.NewAgentInterface("https://agent.example/invoke", sdka2a.TransportProtocolJSONRPC)}, Capabilities: sdka2a.AgentCapabilities{Streaming: true}}
			handler, err := NewProtocolHTTPHandler(card, diagnosticRequestHandler{calls: &calls, err: errors.New(diagnostic)})
			if err != nil {
				t.Fatal(err)
			}
			body := fmt.Sprintf(`{"jsonrpc":"2.0","id":"1","method":%q,"params":{"id":"task-1"}}`, test.method)
			request := httptest.NewRequest(http.MethodPost, "/invoke", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set(sdka2a.SvcParamVersion, test.version)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			wire := response.Body.String()
			if calls.Load() != test.calls || !strings.Contains(wire, fmt.Sprintf(`"code":%d`, test.code)) || strings.Contains(wire, diagnostic) {
				t.Fatalf("custom handler escaped protocol boundary: calls=%d response=%s", calls.Load(), wire)
			}
		})
	}
}

func (f failingTaskStore) Get(context.Context, sdka2a.TaskID) (*taskstore.StoredTask, error) {
	return nil, f.err
}

func TestProtocolBoundaryRedactsStoreDiagnosticsAndPreservesSemanticErrors(t *testing.T) {
	const privateDiagnostic = "private-store-diagnostic"
	for _, test := range []struct {
		name    string
		err     error
		code    int
		message string
	}{
		{name: "ordinary failure", err: errors.New(privateDiagnostic), code: -32603, message: sdka2a.ErrInternalError.Error()},
		{name: "internal protocol error without public message", err: sdka2a.NewError(errors.New(privateDiagnostic), "").WithDetails(map[string]any{"diagnostic": privateDiagnostic}), code: -32603, message: sdka2a.ErrInternalError.Error()},
		{name: "explicit public outcome diagnostic", err: fmt.Errorf("%s: %w", privateDiagnostic, sdka2a.NewError(sdka2a.ErrInternalError, "acceptance is unconfirmed; preserve the request identity")), code: -32603, message: "acceptance is unconfirmed; preserve the request identity"},
		{name: "known task absence", err: sdka2a.ErrTaskNotFound, code: -32001, message: sdka2a.ErrTaskNotFound.Error()},
		{name: "known task absence wrapped in diagnostics", err: fmt.Errorf("%s: %w", privateDiagnostic, sdka2a.ErrTaskNotFound), code: -32001, message: sdka2a.ErrTaskNotFound.Error()},
		{name: "known task absence with unpublished details", err: sdka2a.NewError(sdka2a.ErrTaskNotFound, "").WithDetails(map[string]any{"diagnostic": privateDiagnostic}), code: -32001, message: sdka2a.ErrTaskNotFound.Error()},
	} {
		t.Run(test.name, func(t *testing.T) {
			card := &sdka2a.AgentCard{Name: "test", SupportedInterfaces: []*sdka2a.AgentInterface{sdka2a.NewAgentInterface("https://agent.example/invoke", sdka2a.TransportProtocolJSONRPC)}}
			store := failingTaskStore{Store: taskstore.NewInMemory(&taskstore.InMemoryStoreConfig{Authenticator: a2asrv.NewTaskStoreAuthenticator()}), err: test.err}
			handler, err := NewHTTPHandler(ServerConfig{Agent: chunkedAgent{"hello"}, Card: card, TaskStore: store})
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/invoke", strings.NewReader(`{"jsonrpc":"2.0","id":"1","method":"GetTask","params":{"id":"task-1"}}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set(sdka2a.SvcParamVersion, string(sdka2a.Version))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if strings.Contains(response.Body.String(), privateDiagnostic) {
				t.Fatalf("store diagnostic reached HTTP: %s", response.Body.String())
			}
			var envelope struct {
				Error *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := jsonv2.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error == nil || envelope.Error.Code != test.code || !strings.Contains(envelope.Error.Message, test.message) {
				t.Fatalf("protocol error changed semantics: %s", response.Body.String())
			}
		})
	}
}
