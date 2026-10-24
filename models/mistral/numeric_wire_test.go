package mistral_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
)

func TestToolSchemaNumbersRemainExactOnTheWire(t *testing.T) {
	var body []byte
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var readErr error
		body, readErr = io.ReadAll(r.Body)
		if readErr != nil {
			t.Error(readErr)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	model := newMistralConformanceChat(t, server.URL)
	request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("request")))
	if err != nil {
		t.Fatal(err)
	}
	request.Tools = []chat.ToolDefinition{{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"const":9007199254740993}}}`)}}
	_, err = model.Call(t.Context(), request)
	if err == nil || requests != 1 || !strings.Contains(string(body), `"const":9007199254740993`) {
		t.Fatalf("requests=%d error=%v body=%s", requests, err, body)
	}
}

func TestNativeMetadataNumbersRemainExactOnTheWire(t *testing.T) {
	var body []byte
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	model := newMistralConformanceChat(t, server.URL)
	request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("request")))
	if err != nil {
		t.Fatal(err)
	}
	if setErr := request.Options.Extensions.Set("mistral/request", json.RawMessage(`{"metadata":{"external_id":9007199254740993}}`)); setErr != nil {
		t.Fatal(setErr)
	}
	_, err = model.Call(t.Context(), request)
	if err == nil || requests != 1 || !strings.Contains(string(body), `"external_id":9007199254740993`) || strings.Contains(string(body), "9007199254740992") {
		t.Fatalf("requests=%d error=%v body=%s", requests, err, body)
	}
}
