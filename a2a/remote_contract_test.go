package a2a_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/Tangerg/scope/a2a"
	"github.com/Tangerg/scope/core/tool"
)

func TestRemoteTextDiscoveryRejectsIncompatibleCard(t *testing.T) {
	var endpoint string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"name":"Image Only Agent","supportedInterfaces":[{"url":%q,"protocolBinding":"JSONRPC","protocolVersion":"1.0"}],"defaultInputModes":["application/json"],"defaultOutputModes":["image/png"]}`, endpoint)
	}))
	defer server.Close()
	endpoint = server.URL + "/rpc"
	set, err := a2a.OpenToolSet(t.Context(), a2a.Endpoint{CardURL: server.URL, HTTPClient: server.Client()})
	if set != nil {
		defer func() { _ = set.Close() }()
	}
	if set != nil || !errors.Is(err, a2a.ErrInvalidCard) || !errors.Is(err, a2a.ErrUnsupportedContent) {
		t.Fatalf("incompatible card admission = %v, %v", set, err)
	}
}

func TestRemoteContinuationDoesNotChangeTenantFromContext(t *testing.T) {
	for _, selectedTenant := range []string{"", "selected-tenant"} {
		t.Run("tenant="+selectedTenant, func(t *testing.T) {
			var endpoint string
			var mu sync.Mutex
			var tenants []*string
			var tasks []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet {
					_, _ = fmt.Fprintf(w, `{"name":"Shared Agent","supportedInterfaces":[{"url":%q,"protocolBinding":"JSONRPC","protocolVersion":"1.0","tenant":%q}],"defaultInputModes":["text/plain"],"defaultOutputModes":["text/plain"]}`, endpoint, selectedTenant)
					return
				}
				var request struct {
					ID     json.RawMessage `json:"id"`
					Params struct {
						Tenant  *string `json:"tenant"`
						Message struct {
							TaskID string `json:"taskId"`
						} `json:"message"`
					} `json:"params"`
				}
				if err := jsonv2.UnmarshalRead(r.Body, &request); err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				tenants = append(tenants, request.Params.Tenant)
				tasks = append(tasks, request.Params.Message.TaskID)
				mu.Unlock()
				state := "TASK_STATE_INPUT_REQUIRED"
				if request.Params.Message.TaskID != "" {
					state = "TASK_STATE_COMPLETED"
				}
				_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"task":{"id":"shared-task","contextId":"shared-context","status":{"state":%q,"message":{"messageId":"question","role":"ROLE_AGENT","parts":[{"text":"Choose"}]}}}}}`, request.ID, state)
			}))
			defer server.Close()
			endpoint = server.URL + "/rpc"
			set, err := a2a.OpenToolSet(t.Context(), a2a.Endpoint{CardURL: server.URL, HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = set.Close() }()
			binding, err := tool.Bind(set.Tools()[0])
			if err != nil {
				t.Fatal(err)
			}
			request := tool.Request{Name: binding.Contract().Name(), Arguments: `{"message":"operate"}`}
			invocation, err := binding.Contract().Prepare(request, tool.Admission{})
			if err != nil {
				t.Fatal(err)
			}
			pending, err := binding.Call(sdk.AttachTenant(t.Context(), "tenant-a"), invocation)
			if err != nil || pending.InputRequest == nil {
				t.Fatalf("first call = %+v, %v", pending, err)
			}
			continuation, err := pending.InputRequest.Continuation(json.RawMessage(`"approved"`))
			if err != nil {
				t.Fatal(err)
			}
			resumed, err := binding.Contract().Prepare(request, tool.Admission{Continuation: &continuation})
			if err != nil {
				t.Fatal(err)
			}
			_, callErr := binding.Call(sdk.AttachTenant(t.Context(), "tenant-b"), resumed)
			mu.Lock()
			defer mu.Unlock()
			if callErr != nil || len(tenants) != 2 || !slices.Equal(tasks, []string{"", "shared-task"}) {
				t.Fatalf("continuation lost its target: tenants=%v tasks=%v error=%v", tenants, tasks, callErr)
			}
			for _, tenant := range tenants {
				if selectedTenant == "" && tenant != nil || selectedTenant != "" && (tenant == nil || *tenant != selectedTenant) {
					t.Fatalf("request tenant=%v, want selected interface tenant %q", tenant, selectedTenant)
				}
			}
		})
	}
}

func TestRemoteTextCallsDeclareAcceptedOutputModes(t *testing.T) {
	var endpoint string
	var mu sync.Mutex
	var accepted []string
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = fmt.Fprintf(w, `{"name":"Multimodal Agent","supportedInterfaces":[{"url":%q,"protocolBinding":"JSONRPC","protocolVersion":"1.0"}],"defaultInputModes":["text/plain"],"defaultOutputModes":["text/plain","image/png"]}`, endpoint)
			return
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Params struct {
				Config *struct {
					AcceptedOutputModes []string `json:"acceptedOutputModes"`
				} `json:"configuration"`
			} `json:"params"`
		}
		if err := jsonv2.UnmarshalRead(r.Body, &request); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		calls++
		if request.Params.Config != nil {
			accepted = slices.Clone(request.Params.Config.AcceptedOutputModes)
		}
		textOnly := slices.Equal(accepted, []string{"text/plain"})
		mu.Unlock()
		part := `{"url":"https://images.example/result.png","mediaType":"image/png"}`
		if textOnly {
			part = `{"text":"complete","mediaType":"text/plain"}`
		}
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"task":{"id":"completed-task","contextId":"context","status":{"state":"TASK_STATE_COMPLETED"},"artifacts":[{"artifactId":"result","parts":[%s]}]}}}`, request.ID, part)
	}))
	defer server.Close()
	endpoint = server.URL + "/rpc"
	set, err := a2a.OpenToolSet(t.Context(), a2a.Endpoint{CardURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = set.Close() }()
	binding, err := tool.Bind(set.Tools()[0])
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := binding.Contract().Prepare(tool.Request{Name: binding.Contract().Name(), Arguments: `{"message":"prepare a result"}`}, tool.Admission{})
	if err != nil {
		t.Fatal(err)
	}
	result, callErr := binding.Call(t.Context(), invocation)
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(accepted, []string{"text/plain"}) {
		t.Fatalf("text-only client sent acceptedOutputModes=%v; remote completed %d call, local ErrUnsupportedContent=%t, error=%v", accepted, calls, errors.Is(callErr, a2a.ErrUnsupportedContent), callErr)
	}
	if callErr != nil {
		t.Fatal(callErr)
	}
	if text, present := result.Output.Text(); !present || text != "complete" || calls != 1 {
		t.Fatalf("negotiated result=%q present=%t calls=%d", text, present, calls)
	}
}

func TestRemoteTextMIMETypes(t *testing.T) {
	for _, test := range []struct {
		name     string
		cardMode string
		partMode string
		valid    bool
	}{
		{name: "plain text", cardMode: "text/plain", partMode: "text/plain", valid: true},
		{name: "card case", cardMode: "TEXT/PLAIN", partMode: "text/plain", valid: true},
		{name: "card parameters", cardMode: "text/plain; charset=utf-8", partMode: "text/plain", valid: true},
		{name: "part case", cardMode: "text/plain", partMode: "TEXT/PLAIN", valid: true},
		{name: "part parameters", cardMode: "text/plain", partMode: "text/plain; charset=utf-8", valid: true},
		{name: "other subtype", cardMode: "text/plain", partMode: "text/markdown"},
		{name: "malformed parameters", cardMode: "text/plain", partMode: "text/plain; charset"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var endpoint string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet {
					_, _ = fmt.Fprintf(w, `{"name":"Text Agent","supportedInterfaces":[{"url":%q,"protocolBinding":"JSONRPC","protocolVersion":"1.0"}],"defaultInputModes":[%q],"defaultOutputModes":[%q]}`, endpoint, test.cardMode, test.cardMode)
					return
				}
				var request struct {
					ID json.RawMessage `json:"id"`
				}
				if err := jsonv2.UnmarshalRead(r.Body, &request); err != nil {
					t.Error(err)
					return
				}
				_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"task":{"id":"completed-task","contextId":"context","status":{"state":"TASK_STATE_COMPLETED"},"artifacts":[{"artifactId":"result","parts":[{"text":"complete","mediaType":%q}]}]}}}`, request.ID, test.partMode)
			}))
			defer server.Close()
			endpoint = server.URL + "/rpc"
			set, err := a2a.OpenToolSet(t.Context(), a2a.Endpoint{CardURL: server.URL, HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = set.Close() }()
			binding, err := tool.Bind(set.Tools()[0])
			if err != nil {
				t.Fatal(err)
			}
			invocation, err := binding.Contract().Prepare(tool.Request{Name: binding.Contract().Name(), Arguments: `{"message":"prepare a result"}`}, tool.Admission{})
			if err != nil {
				t.Fatal(err)
			}
			result, err := binding.Call(t.Context(), invocation)
			if !test.valid {
				if !errors.Is(err, a2a.ErrUnsupportedContent) {
					t.Fatalf("Call = %+v, %v; want unsupported content", result, err)
				}
				return
			}
			text, present := result.Output.Text()
			if err != nil || !present || text != "complete" {
				t.Fatalf("Call = %q, %v, %v", text, present, err)
			}
		})
	}
}
