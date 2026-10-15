package a2a

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
)

func wireA2AServer(t *testing.T, agent Agent, store taskstore.Store) *httptest.Server {
	t.Helper()
	handler, err := NewHTTPHandler(ServerConfig{Agent: agent, TaskStore: store, Card: &sdka2a.AgentCard{
		Name: "wire", SupportedInterfaces: []*sdka2a.AgentInterface{sdka2a.NewAgentInterface("https://agent.example/rpc", sdka2a.TransportProtocolJSONRPC)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

// Requests use independent JSON values rather than the SDK's request encoder.
func wireA2ARPC(t *testing.T, endpoint, method, params string) json.RawMessage {
	t.Helper()
	body, err := jsonv2.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      string          `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}{"2.0", "wire-9007199254740993", method, json.RawMessage(params)})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint+"/rpc", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	encoded, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		ID     string          `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := jsonv2.Unmarshal(encoded, &envelope); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || len(envelope.Error) > 0 || envelope.ID != "wire-9007199254740993" {
		t.Fatalf("RPC %s: status %d, response %s", method, response.StatusCode, encoded)
	}
	return envelope.Result
}

func TestWireFollowUpRetainsStoredTask(t *testing.T) {
	store := taskstore.NewInMemory(&taskstore.InMemoryStoreConfig{Authenticator: a2asrv.NewTaskStoreAuthenticator()})
	first := a2asrv.AgentExecutorFunc(func(ctx context.Context, execution *a2asrv.ExecutorContext) iter.Seq2[sdka2a.Event, error] {
		return func(yield func(sdka2a.Event, error) bool) {
			if !yield(sdka2a.NewSubmittedTask(execution, execution.Message), nil) {
				return
			}
			if !yield(sdka2a.NewArtifactEvent(execution, sdka2a.NewTextPart("retained evidence")), nil) {
				return
			}
			yield(sdka2a.NewStatusUpdateEvent(execution, sdka2a.TaskStateInputRequired, nil), nil)
		}
	})
	initial := httptest.NewServer(a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(first, a2asrv.WithTaskStore(store))))
	t.Cleanup(initial.Close)
	initialJSON := wireA2ARPC(t, initial.URL, "SendMessage", `{"message":{"messageId":"turn-1","role":"ROLE_USER","parts":[{"text":"first input"}]}}`)
	var pending struct {
		Task *sdka2a.Task `json:"task"`
	}
	if err := jsonv2.Unmarshal(initialJSON, &pending); err != nil {
		t.Fatal(err)
	}
	if pending.Task == nil || pending.Task.Status.State != sdka2a.TaskStateInputRequired || len(pending.Task.History) != 1 || len(pending.Task.Artifacts) != 1 {
		t.Fatalf("initial task = %s", initialJSON)
	}
	server := wireA2AServer(t, chunkedAgent{"resumed answer"}, store)
	encoded := wireA2ARPC(t, server.URL, "SendMessage", fmt.Sprintf(`{"message":{"messageId":"turn-2","taskId":%q,"contextId":%q,"role":"ROLE_USER","parts":[{"text":"second input"}]}}`, pending.Task.ID, pending.Task.ContextID))
	var result struct {
		Task *sdka2a.Task `json:"task"`
	}
	if err := jsonv2.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	if result.Task == nil || result.Task.Status.State != sdka2a.TaskStateCompleted {
		t.Fatalf("follow-up result = %s", encoded)
	}
	if result.Task.ID != pending.Task.ID || result.Task.ContextID != pending.Task.ContextID || len(result.Task.History) != 2 || result.Task.History[0].ID != "turn-1" || result.Task.History[1].ID != "turn-2" || len(result.Task.Artifacts) != 2 || result.Task.Artifacts[0].ID != pending.Task.Artifacts[0].ID {
		t.Fatalf("follow-up replaced the stored task: %s", encoded)
	}
	projection := textProjection{}
	if projection.parts(result.Task.History[0].Parts) != "first input" || projection.parts(result.Task.History[1].Parts) != "second input" || projection.parts(result.Task.Artifacts[0].Parts) != "retained evidence" {
		t.Fatalf("follow-up changed retained content: %s", encoded)
	}

}

type cancellableWireAgent struct {
	started chan struct{}
	stopped chan struct{}
	release chan struct{}
}

func (c *cancellableWireAgent) Run(ctx context.Context, input string) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		defer close(c.stopped)
		if !yield("partial evidence", nil) {
			return
		}
		close(c.started)
		select {
		case <-ctx.Done():
			yield("", ctx.Err())
		case <-c.release:
		}
	}
}

func TestWireCancelTaskStopsExecutionAndRetainsTerminalState(t *testing.T) {
	agent := &cancellableWireAgent{started: make(chan struct{}), stopped: make(chan struct{}), release: make(chan struct{})}
	store := taskstore.NewInMemory(&taskstore.InMemoryStoreConfig{Authenticator: a2asrv.NewTaskStoreAuthenticator()})
	server := wireA2AServer(t, agent, store)
	t.Cleanup(func() { close(agent.release); waitWireSignal(t, agent.stopped) })
	encoded := wireA2ARPC(t, server.URL, "SendMessage", `{"configuration":{"returnImmediately":true},"message":{"messageId":"cancel-input","role":"ROLE_USER","parts":[{"text":"work until canceled"}]}}`)
	var submitted struct {
		Task *sdka2a.Task `json:"task"`
	}
	if err := jsonv2.Unmarshal(encoded, &submitted); err != nil {
		t.Fatal(err)
	}
	if submitted.Task == nil || submitted.Task.ID == "" {
		t.Fatalf("submitted = %s", encoded)
	}
	waitWireSignal(t, agent.started)
	params := fmt.Sprintf(`{"id":%q}`, submitted.Task.ID)
	encoded = wireA2ARPC(t, server.URL, "CancelTask", params)
	var canceled sdka2a.Task
	if err := jsonv2.Unmarshal(encoded, &canceled); err != nil {
		t.Fatal(err)
	}
	waitWireSignal(t, agent.stopped)
	if canceled.ID != submitted.Task.ID || canceled.Status.State != sdka2a.TaskStateCanceled || len(canceled.Artifacts) != 1 || (textProjection{}).parts(canceled.Artifacts[0].Parts) != "partial evidence" {
		t.Fatalf("canceled task lost its state or evidence: %s", encoded)
	}
	encoded = wireA2ARPC(t, server.URL, "GetTask", params)
	var retained sdka2a.Task
	if err := jsonv2.Unmarshal(encoded, &retained); err != nil {
		t.Fatal(err)
	}
	if retained.Status.State != sdka2a.TaskStateCanceled || retained.ID != canceled.ID || len(retained.Artifacts) != 1 {
		t.Fatalf("execution advanced after cancellation: %s", encoded)
	}
}

func waitWireSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("wire execution did not reach the required lifecycle boundary")
	}
}
