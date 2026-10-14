package a2a

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
)

type diagnosticAgent struct{ continued *bool }

func (d diagnosticAgent) Run(context.Context, string) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		if !yield("", errors.New("private-http-error-marker")) {
			return
		}
		*d.continued = true
		yield("unexpected completion", nil)
	}
}

func TestHTTPFailureDoesNotPublishInternalDiagnostics(t *testing.T) {
	continued := false
	card := &sdka2a.AgentCard{
		Name: "test", SupportedInterfaces: []*sdka2a.AgentInterface{sdka2a.NewAgentInterface("https://agent.example/invoke", sdka2a.TransportProtocolJSONRPC)},
	}
	handler, err := NewHTTPHandler(ServerConfig{
		Agent: diagnosticAgent{continued: &continued}, Card: card,
		TaskStore: taskstore.NewInMemory(&taskstore.InMemoryStoreConfig{Authenticator: a2asrv.NewTaskStoreAuthenticator()}),
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := jsonv2.Marshal(struct {
		JSONRPC string                     `json:"jsonrpc"`
		ID      string                     `json:"id"`
		Method  string                     `json:"method"`
		Params  *sdka2a.SendMessageRequest `json:"params"`
	}{"2.0", "1", "SendMessage", &sdka2a.SendMessageRequest{Message: sdka2a.NewMessage(sdka2a.MessageRoleUser, sdka2a.NewTextPart("hello"))}})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/invoke", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "private-http-error-marker") {
		t.Fatalf("failure response status=%d body=%s", response.Code, response.Body.String())
	}
	var result struct {
		Result struct {
			Task *sdka2a.Task `json:"task"`
		} `json:"result"`
	}
	if err := jsonv2.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Result.Task == nil || result.Result.Task.Status.State != sdka2a.TaskStateFailed || continued {
		t.Fatalf("failure result=%+v iteration continued=%t body=%s", result.Result.Task, continued, response.Body.String())
	}
	if detail := (textProjection{}).parts(result.Result.Task.Status.Message.Parts); detail != "agent execution failed" {
		t.Fatalf("public failure message=%q", detail)
	}
}

type nilSequenceAgent struct{}

func (nilSequenceAgent) Run(context.Context, string) iter.Seq2[string, error] { return nil }

type chunkedAgent []string

func (c chunkedAgent) Run(context.Context, string) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		for _, chunk := range c {
			if !yield(chunk, nil) {
				return
			}
		}
	}
}

func TestExecutorTurnsNilAgentSequenceIntoFailedTask(t *testing.T) {
	projection := textProjection{}
	exec, err := newExecutor(nilSequenceAgent{})
	if err != nil {
		t.Fatal(err)
	}
	execCtx := &a2asrv.ExecutorContext{
		TaskID:    "task-1",
		ContextID: "context-1",
		Message:   sdka2a.NewMessage(sdka2a.MessageRoleUser, sdka2a.NewTextPart("hello")),
	}

	var final *sdka2a.TaskStatusUpdateEvent
	for event, eventErr := range exec.Execute(t.Context(), execCtx) {
		if eventErr != nil {
			t.Fatalf("Execute event error: %v", eventErr)
		}
		if status, ok := event.(*sdka2a.TaskStatusUpdateEvent); ok {
			final = status
		}
	}
	if final == nil || final.Status.State != sdka2a.TaskStateFailed || final.Status.Message == nil {
		t.Fatalf("final event = %#v, want failed status with message", final)
	}
	if detail := projection.parts(final.Status.Message.Parts); detail != "agent execution failed" {
		t.Fatalf("failure detail = %q", detail)
	}
}

func TestExecutorStreamsOneArtifact(t *testing.T) {
	projection := textProjection{}
	exec, err := newExecutor(chunkedAgent{"hello", " ", "world"})
	if err != nil {
		t.Fatal(err)
	}
	execCtx := &a2asrv.ExecutorContext{
		TaskID:    "task-1",
		ContextID: "context-1",
		Message:   sdka2a.NewMessage(sdka2a.MessageRoleUser, sdka2a.NewTextPart("hello")),
	}

	var updates []*sdka2a.TaskArtifactUpdateEvent
	for event, eventErr := range exec.Execute(t.Context(), execCtx) {
		if eventErr != nil {
			t.Fatalf("Execute event error: %v", eventErr)
		}
		if update, ok := event.(*sdka2a.TaskArtifactUpdateEvent); ok {
			updates = append(updates, update)
		}
	}
	if len(updates) != 3 {
		t.Fatalf("artifact updates = %d, want 3", len(updates))
	}
	artifactID := updates[0].Artifact.ID
	if artifactID == "" || updates[0].Append {
		t.Fatalf("first artifact update = %#v, want a new artifact", updates[0])
	}
	var text strings.Builder
	for i, update := range updates {
		if update.Artifact.ID != artifactID {
			t.Fatalf("artifact update %d ID = %q, want %q", i, update.Artifact.ID, artifactID)
		}
		if i > 0 && !update.Append {
			t.Fatalf("artifact update %d Append = false, want true", i)
		}
		text.WriteString(projection.parts(update.Artifact.Parts))
	}
	if got := text.String(); got != "hello world" {
		t.Fatalf("artifact text = %q, want %q", got, "hello world")
	}
}
