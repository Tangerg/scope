package a2a_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tangerg/scope/a2a"
	"github.com/Tangerg/scope/core/tool"
)

func TestRemoteCallSendsItsIdempotencyKeyAsTheMessageID(t *testing.T) {
	var endpoint string
	var messageIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.Method == http.MethodGet {
			url, err := jsonv2.Marshal(endpoint)
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = fmt.Fprintf(writer, `{"name":"Ledger Agent","supportedInterfaces":[{"url":%s,"protocolBinding":"JSONRPC","protocolVersion":"1.0"}],"defaultInputModes":["text/plain"],"defaultOutputModes":["text/plain"]}`, url)
			return
		}
		var envelope struct {
			ID     json.RawMessage `json:"id"`
			Params struct {
				Message struct {
					ID string `json:"messageId"`
				} `json:"message"`
			} `json:"params"`
		}
		if err := jsonv2.UnmarshalRead(request.Body, &envelope); err != nil {
			t.Error(err)
			return
		}
		messageIDs = append(messageIDs, envelope.Params.Message.ID)
		_, _ = fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"result":{"message":{"messageId":"reply","role":"ROLE_AGENT","parts":[{"text":"recorded"}]}}}`, envelope.ID)
	}))
	t.Cleanup(server.Close)
	endpoint = server.URL + "/rpc"
	tools, err := a2a.OpenToolSet(t.Context(), a2a.Endpoint{CardURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := tools.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	binding, err := tool.Bind(tools.Tools()[0])
	if err != nil {
		t.Fatal(err)
	}
	if binding.Contract().Effect() != tool.EffectUnsafe {
		t.Fatal("message identity was mistaken for an endpoint replay guarantee")
	}
	request := tool.Request{Name: binding.Contract().Definition().Name, Arguments: `{"message":"record"}`}
	for _, admission := range []tool.Admission{{IdempotencyKey: "call-1"}, {IdempotencyKey: "call-1"}, {}} {
		invocation, prepareErr := binding.Contract().Prepare(request, admission)
		if prepareErr != nil {
			t.Fatal(prepareErr)
		}
		if _, callErr := binding.Call(t.Context(), invocation); callErr != nil {
			t.Fatal(callErr)
		}
	}
	if len(messageIDs) != 3 || messageIDs[0] != "call-1" || messageIDs[1] != "call-1" || messageIDs[2] == "" || messageIDs[2] == "call-1" {
		t.Fatalf("message IDs = %q; a replayed call must resend its key and an unkeyed call must get a fresh ID", messageIDs)
	}
}
