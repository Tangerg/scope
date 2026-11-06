package a2a_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/a2a"
	"github.com/Tangerg/scope/core/tool"
)

func TestContinuationSurvivesReconstructionOnlyWithinItsAuthority(t *testing.T) {
	var endpoint string
	var calls atomic.Int32
	var advertisedURL, advertisedTenant atomic.Value
	advertisedTenant.Store("")
	var reply atomic.Pointer[continuationReply]
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = fmt.Fprintf(w, `{"name":"Stable Agent","supportedInterfaces":[{"url":%q,"tenant":%q,"protocolBinding":"JSONRPC","protocolVersion":"1.0"}],"defaultInputModes":["text/plain"],"defaultOutputModes":["text/plain"]}`, advertisedURL.Load(), advertisedTenant.Load())
			return
		}
		calls.Add(1)
		var request struct {
			ID     json.RawMessage `json:"id"`
			Params struct {
				Message struct {
					ID        string `json:"messageId"`
					TaskID    string `json:"taskId"`
					ContextID string `json:"contextId"`
				} `json:"message"`
			} `json:"params"`
		}
		if err := jsonv2.UnmarshalRead(r.Body, &request); err != nil {
			t.Error(err)
			return
		}
		state := "TASK_STATE_INPUT_REQUIRED"
		if request.Params.Message.TaskID != "" {
			if request.Params.Message.TaskID != "task" || request.Params.Message.ContextID != "context" || request.Params.Message.ID == "" {
				t.Errorf("lost original task identity: %+v", request.Params.Message)
			}
			state = "TASK_STATE_COMPLETED"
			if target := reply.Load(); target != nil {
				_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"message":{"messageId":"reply","taskId":%q,"contextId":%q,"role":"ROLE_AGENT","parts":[{"text":"accepted"}]}}}`, request.ID, target.task, target.context)
				return
			}
		}
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"task":{"id":"task","contextId":"context","status":{"state":%q,"message":{"messageId":"question","role":"ROLE_AGENT","parts":[{"text":"Approve?"}]}}}}}`, request.ID, state)
	}))
	defer server.Close()
	endpoint = server.URL + "/rpc"
	advertisedURL.Store(endpoint)
	config := a2a.Endpoint{CardURL: server.URL, HTTPClient: server.Client(), ContinuationIdentity: "principal-a/revision-1"}
	open := func(t *testing.T, config a2a.Endpoint) (*a2a.ToolSet, tool.Binding) {
		t.Helper()
		set, err := a2a.OpenToolSet(t.Context(), config)
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
		return set, bound
	}
	set, bound := open(t, config)
	request := tool.Request{Name: bound.Contract().Name(), Arguments: `{"message":"start"}`}
	invocation, err := bound.Contract().Prepare(request, tool.Admission{})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := bound.Call(t.Context(), invocation)
	if err != nil || pending.InputRequest == nil {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	if closeErr := set.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	saved := pending.InputRequest
	restored, err := tool.NewInputRequest(tool.InputRequestConfig{Prompt: saved.Prompt(), ResponseSchema: saved.ResponseSchema(), ContinuationState: saved.State()})
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := restored.Continuation(json.RawMessage(`"approved"`))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*a2a.Endpoint)
		valid  bool
	}{
		{"different principal", func(c *a2a.Endpoint) { c.ContinuationIdentity = "principal-b/revision-1" }, false},
		{"different revision", func(c *a2a.Endpoint) { c.ContinuationIdentity = "principal-a/revision-2" }, false},
		{"different discovery", func(c *a2a.Endpoint) { c.CardURL += "/other" }, false},
		{"different tool", func(c *a2a.Endpoint) { c.Name = "other" }, false},
		{"different remote URL", func(*a2a.Endpoint) { advertisedURL.Store(server.URL + "/other-rpc") }, false},
		{"different remote tenant", func(*a2a.Endpoint) { advertisedTenant.Store("another-tenant") }, false},
		{"ephemeral", func(c *a2a.Endpoint) { c.ContinuationIdentity = "" }, false},
		{"same authority", func(*a2a.Endpoint) {}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			advertisedURL.Store(endpoint)
			advertisedTenant.Store("")
			candidate := config
			test.change(&candidate)
			_, reopened := open(t, candidate)
			nextRequest := request
			nextRequest.Name = reopened.Contract().Name()
			next, err := reopened.Contract().Prepare(nextRequest, tool.Admission{Continuation: &continuation})
			if err != nil {
				t.Fatal(err)
			}
			before := calls.Load()
			_, err = reopened.Call(t.Context(), next)
			if test.valid {
				if err != nil || calls.Load() != before+1 {
					t.Fatalf("reconstruction did not resume: calls=%d err=%v", calls.Load(), err)
				}
			} else if !errors.Is(err, a2a.ErrContinuationBinding) || calls.Load() != before {
				t.Fatalf("foreign continuation dispatched: calls=%d err=%v", calls.Load(), err)
			}
		})
	}
	for _, test := range []struct {
		name   string
		target continuationReply
		valid  bool
	}{
		{"same task and context", continuationReply{"task", "context"}, true},
		{"foreign task", continuationReply{"other", "context"}, false},
		{"foreign context", continuationReply{"task", "other"}, false},
		{"missing task", continuationReply{"", "context"}, false},
		{"missing context", continuationReply{"task", ""}, false},
	} {
		t.Run("message/"+test.name, func(t *testing.T) {
			reply.Store(&test.target)
			_, reopened := open(t, config)
			next, err := reopened.Contract().Prepare(request, tool.Admission{Continuation: &continuation})
			if err != nil {
				t.Fatal(err)
			}
			before := calls.Load()
			_, err = reopened.Call(t.Context(), next)
			if calls.Load() != before+1 || test.valid && err != nil || !test.valid && !errors.Is(err, a2a.ErrContinuationBinding) {
				t.Fatalf("continuation message target=%+v calls=%d error=%v", test.target, calls.Load(), err)
			}
		})
	}
}

type continuationReply struct{ task, context string }
