package a2a

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"iter"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"

	"github.com/Tangerg/scope/core/tool"
)

func TestMalformedWaitExtensionNeverFallsBackToUnaddressedText(t *testing.T) {
	r := remoteTool{}
	const schema = `{"type":"string"}`
	fields := func(w, s any) map[string]any { return map[string]any{WaitIDMetadataKey: w, AnswerSchemaMetadataKey: s} }
	for _, tc := range []struct {
		name  string
		meta  map[string]any
		part  *sdk.Part
		cause error
	}{
		{"schema without address", map[string]any{AnswerSchemaMetadataKey: schema}, sdk.NewTextPart("question"), ErrInvalidResult},
		{"address without schema", map[string]any{WaitIDMetadataKey: "w"}, sdk.NewTextPart("question"), ErrInvalidResult},
		{"nonnative address", fields(42, schema), sdk.NewTextPart("question"), ErrInvalidResult},
		{"blank address", fields(" ", schema), sdk.NewTextPart("question"), ErrInvalidResult},
		{"unencodable schema", fields("w", true), sdk.NewTextPart("question"), ErrInvalidResult},
		{"invalid schema", fields("w", "{"), sdk.NewTextPart("question"), nil},
		{"binary is not a question", fields("w", schema), sdk.NewRawPart([]byte{255}), ErrUnsupportedContent},
		{"incorrect MIME", fields("w", schema), &sdk.Part{Content: sdk.Text("html"), MediaType: "text/html"}, ErrUnsupportedContent},
		{"malformed JSON question", fields("w", schema), &sdk.Part{Content: sdk.Text("{"), MediaType: "application/json"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := sdk.NewMessage(sdk.MessageRoleAgent, tc.part)
			msg.Metadata = tc.meta
			task := &sdk.Task{ID: "remote-task", ContextID: "context", Status: sdk.TaskStatus{State: sdk.TaskStateInputRequired, Message: msg}}
			res, err := r.awaitInput(task, tool.Invocation{})
			if err == nil || res.InputRequest != nil || tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Fatalf("input authority created from malformed remote evidence: %+v %v", res, err)
			}
		})
	}
}
func TestProtocolHandlerRejectsMissingOwners(t *testing.T) {
	var missing *a2asrv.InterceptedHandler
	if handler, err := NewProtocolHTTPHandler(&sdk.AgentCard{}, missing); err == nil || handler != nil {
		t.Fatal("typed nil handler advertised capabilities")
	}
	raw := a2asrv.NewHandler(a2asrv.AgentExecutorFunc(func(context.Context, *a2asrv.ExecutorContext) iter.Seq2[sdk.Event, error] { return nil }))
	if handler, err := NewProtocolHTTPHandler(nil, raw); !errors.Is(err, ErrNilCard) || handler != nil {
		t.Fatal("endpoint without identity", err)
	}
}

// The public route serves the supplied handler's durable task projections.
// A typed JSON reply carries the original wait, task, context and message ID.
// The wire count also detects accidental dispatch after local admission errors.
func TestProtocolHandlerAndRemoteToolShareAnExactStructuredWait(t *testing.T) {
	const question = `{"ask":"exact amount","minimum":9007199254740993}`
	const schema = `{"type":"object","required":["amount"],"properties":{"amount":{"type":"integer"}},"additionalProperties":false}`
	const answer = `{"amount":9007199254740993}`
	s := httptest.NewUnstartedServer(nil)
	addr := "http://" + s.Listener.Addr().String() + "/rpc"
	card := &sdk.AgentCard{Name: "priced", Description: "Choose an exact amount", SupportedInterfaces: []*sdk.AgentInterface{sdk.NewAgentInterface(addr, sdk.TransportProtocolJSONRPC)}}
	var calls atomic.Int32
	var orig sdk.TaskInfo
	exec := a2asrv.AgentExecutorFunc(func(ctx context.Context, e *a2asrv.ExecutorContext) iter.Seq2[sdk.Event, error] {
		return func(yield func(sdk.Event, error) bool) {
			calls.Add(1)
			if e.StoredTask == nil {
				orig = e.TaskInfo()
				if !yield(sdk.NewSubmittedTask(e, e.Message), nil) {
					return
				}
				p := sdk.NewTextPart(question)
				p.MediaType = "application/json"
				m := sdk.NewMessageForTask(sdk.MessageRoleAgent, e, p)
				m.Metadata = map[string]any{WaitIDMetadataKey: "wait:price", AnswerSchemaMetadataKey: schema}
				yield(sdk.NewStatusUpdateEvent(e, sdk.TaskStateInputRequired, m), nil)
				return
			}
			m := e.Message
			if e.TaskInfo() != orig || m.ID == "" || m.TaskID != orig.TaskID || m.ContextID != orig.ContextID || m.Metadata[WaitIDMetadataKey] != "wait:price" || len(m.Parts) != 1 || m.Parts[0].MediaType != "application/json" || m.Parts[0].Text() != answer {
				t.Errorf("misbound or lossy reply: %+v", m)
			}
			if !yield(sdk.NewArtifactEvent(e, sdk.NewTextPart("accepted")), nil) {
				return
			}
			yield(sdk.NewStatusUpdateEvent(e, sdk.TaskStateCompleted, nil), nil)
		}
	})
	store := taskstore.NewInMemory(&taskstore.InMemoryStoreConfig{Authenticator: a2asrv.NewTaskStoreAuthenticator()})
	raw := a2asrv.NewHandler(exec, a2asrv.WithTaskStore(store))
	service, err := NewProtocolHTTPHandler(card, raw)
	if err != nil {
		t.Fatal(err)
	}
	s.Config.Handler = service
	s.Start()
	t.Cleanup(s.Close)
	card.SupportedInterfaces[0].URL = "https://changed.invalid/replaced"
	set, err := OpenToolSet(t.Context(), Endpoint{CardURL: s.URL, HTTPClient: s.Client()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := set.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	bound, err := tool.Bind(set.Tools()[0])
	if err != nil {
		t.Fatal(err)
	}
	req := tool.Request{Name: bound.Contract().Definition().Name, Arguments: `{"message":"quote"}`}
	inv, err := bound.Contract().Prepare(req, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := bound.Call(t.Context(), inv)
	if err != nil || first.InputRequest == nil {
		t.Fatal(first, err)
	}
	wait := first.InputRequest
	if string(wait.Prompt()) != question || calls.Load() != 1 {
		t.Fatalf("lost question or dispatched twice: %s, %d", wait.Prompt(), calls.Load())
	}
	if _, err = wait.Continuation(json.RawMessage(`{"amount":"wrong"}`)); err == nil {
		t.Fatal("invalid JSON answer passed schema admission")
	}
	// Restoring an unknown continuation-state format must not send a network call.
	var modified map[string]json.RawMessage
	if err = jsonv2.Unmarshal(wait.State(), &modified); err != nil {
		t.Fatal(err)
	}
	modified["unknown"] = json.RawMessage("true")
	state, err := jsonv2.Marshal(modified)
	if err != nil {
		t.Fatal(err)
	}
	future, err := tool.NewInputRequest(tool.InputRequestConfig{Prompt: wait.Prompt(), ResponseSchema: wait.ResponseSchema(), ContinuationState: state})
	if err != nil {
		t.Fatal(err)
	}
	bad, err := future.Continuation(json.RawMessage(answer))
	if err != nil {
		t.Fatal(err)
	}
	next, err := bound.Contract().Prepare(req, &bad)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = bound.Call(t.Context(), next); !errors.Is(err, ErrContinuationBinding) || calls.Load() != 1 {
		t.Fatal("unsupported state dispatched a request", err, calls.Load())
	}
	c, err := wait.Continuation(json.RawMessage(answer))
	if err != nil {
		t.Fatal(err)
	}
	next, err = bound.Contract().Prepare(req, &c)
	if err != nil {
		t.Fatal(err)
	}
	final, err := bound.Call(t.Context(), next)
	if err != nil {
		t.Fatal(err)
	}
	if text, ok := final.Output.Text(); !ok || text != "accepted" || calls.Load() != 2 {
		t.Fatalf("terminal state: %q calls=%d", text, calls.Load())
	}
}
