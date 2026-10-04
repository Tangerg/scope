package openai_test

import (
	"bytes"
	"encoding/binary"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/chat"
)

func TestResponsesReasoningReplayKeepsSegmentOwners(t *testing.T) {
	firstItem := `{"type":"reasoning","id":"rs-1","summary":[{"type":"summary_text","text":"first summary","native_child":7},{"type":"summary_text","text":""}],"content":[{"type":"reasoning_text","text":"original content"}],"encrypted_content":"complete encrypted content","status":"completed","vendor_field":9}`
	secondItem := `{"type":"reasoning","id":"rs-2","summary":[],"encrypted_content":null,"status":"completed"}`
	events := []string{
		`{"type":"response.output_item.added","item":{"type":"reasoning","id":"rs-1","summary":[],"encrypted_content":"incomplete encrypted content","status":"in_progress"}}`,
		`{"type":"response.reasoning_summary_part.added","item_id":"rs-1","summary_index":0,"part":{"type":"summary_text","text":""}}`,
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs-1","summary_index":0,"delta":"first "}`,
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs-1","summary_index":0,"delta":"summary"}`,
		`{"type":"response.reasoning_summary_part.added","item_id":"rs-1","summary_index":1,"part":{"type":"summary_text","text":""}}`,
		`{"type":"response.content_part.added","item_id":"rs-1","content_index":0,"part":{"type":"reasoning_text","text":""}}`,
		`{"type":"response.reasoning_text.delta","item_id":"rs-1","content_index":0,"delta":"original content"}`,
		`{"type":"response.output_item.done","item":` + firstItem + `}`,
		`{"type":"response.output_item.done","item":` + secondItem + `}`,
		`{"type":"response.output_text.delta","item_id":"answer","content_index":0,"delta":"answer"}`,
		`{"type":"response.completed","response":{"id":"response-1","model":"model","status":"completed","output":[` + firstItem + `,` + secondItem + `,{"type":"message","id":"answer","role":"assistant","status":"completed","content":[{"type":"output_text","text":"answer","annotations":[]}]}]}}`,
	}
	var captured struct {
		Input []map[string]any `json:"input"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := jsonv2.UnmarshalRead(request.Body, &captured); err != nil {
			t.Error(err)
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		if strings.HasSuffix(request.URL.Path, "/input_tokens") {
			writer.Header().Set("Content-Type", "application/json")
			fmt.Fprint(writer, `{"input_tokens":10}`)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		for _, event := range events {
			fmt.Fprintf(writer, "data: %s\n\n", event)
		}
	}))
	t.Cleanup(server.Close)
	model := newResponsesModel(t, server.URL, "model")
	user := chat.NewUserMessage(chat.NewTextPart("question"))
	response, err := model.Call(t.Context(), &chat.Request{Messages: []chat.Message{user}})
	if err != nil {
		t.Fatal(err)
	}
	message := response.Output.Message.Clone()
	if len(message.Parts) != 5 || message.Parts[0].Text != "first summary" || message.Parts[1].Text != "" || message.Parts[2].Text != "original content" || message.Parts[3].Text != "" || message.Parts[4].Text != "answer" {
		t.Fatalf("reasoning boundaries changed: %#v", message.Parts)
	}
	message.Parts[0].Text = "edited summary"
	message.Parts[2].Text = "edited content"
	for _, part := range message.Parts {
		if bytes.Contains(part.ReasoningState, []byte("first summary")) || bytes.Contains(part.ReasoningState, []byte("original content")) {
			t.Fatal("reasoning state retains a Core text copy")
		}
	}
	encoded, err := jsonv2.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if err := jsonv2.Unmarshal(encoded, &message); err != nil {
		t.Fatal(err)
	}
	request := &chat.Request{Messages: []chat.Message{user, message}}
	if _, err := model.Call(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	assertResponsesReasoningInput(t, captured.Input)
	if count, err := model.CountInputTokens(t.Context(), request); err != nil || count != 10 {
		t.Fatalf("CountInputTokens = %d, %v", count, err)
	}
	assertResponsesReasoningInput(t, captured.Input)
}

func assertResponsesReasoningInput(t *testing.T, input []map[string]any) {
	t.Helper()
	if len(input) != 4 || input[1]["id"] != "rs-1" || input[2]["id"] != "rs-2" || input[3]["role"] != "assistant" {
		t.Fatalf("reasoning item order changed: %#v", input)
	}
	first, second := input[1], input[2]
	if first["encrypted_content"] != "complete encrypted content" || first["status"] != "completed" || first["vendor_field"] != float64(9) {
		t.Fatalf("completed native fields changed: %#v", first)
	}
	summary := first["summary"].([]any)
	content := first["content"].([]any)
	if len(summary) != 2 || summary[0].(map[string]any)["text"] != "edited summary" || summary[0].(map[string]any)["native_child"] != float64(7) || summary[1].(map[string]any)["text"] != "" || len(content) != 1 || content[0].(map[string]any)["text"] != "edited content" {
		t.Fatalf("reasoning text did not follow Core: %#v", first)
	}
	_, encryptedPresent := second["encrypted_content"]
	if !encryptedPresent || second["encrypted_content"] != nil || len(second["summary"].([]any)) != 0 {
		t.Fatalf("opaque native item changed: %#v", second)
	}
}

func TestResponsesRejectInvalidReasoningHistoryBeforeIO(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(writer, "unexpected provider call", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	model := newResponsesModel(t, server.URL, "model")
	for _, test := range []struct {
		name   string
		text   string
		states []string
	}{
		{"old full item", "current", []string{`{"id":"rs-1","type":"reasoning","summary":[{"type":"summary_text","text":"old copy"}]}`}},
		{"stored text copy", "current", []string{`{"item":{"id":"rs-1","type":"reasoning","summary":[{"type":"summary_text","text":"second owner"}]}}`}},
		{"missing completed item", "current", []string{`{"segment":{"item_id":"rs-1","kind":"summary","index":0}}`}},
		{"missing Core segment", "", []string{`{"item":{"id":"rs-1","type":"reasoning","summary":[{"type":"summary_text"}]}}`}},
		{"text on opaque item", "unexpected", []string{`{"item":{"id":"rs-1","type":"reasoning","summary":[]}}`}},
		{"mixed segment identities", "current", []string{`{"segment":{"item_id":"rs-1","kind":"summary","index":0}}`, `{"segment":{"item_id":"rs-1","kind":"content","index":0}}`}},
		{"mixed item identities", "current", []string{`{"segment":{"item_id":"rs-1","kind":"summary","index":0}}`, `{"item":{"id":"rs-2","type":"reasoning","summary":[]}}`}},
		{"duplicate completed states", "", []string{`{"item":{"id":"rs-1","type":"reasoning","summary":[]}}`, `{"item":{"id":"rs-1","type":"reasoning","summary":[]}}`}},
		{"invalid segment index", "current", []string{`{"segment":{"item_id":"rs-1","kind":"summary","index":-1}}`}},
		{"unknown state field", "", []string{`{"item":{"id":"rs-1","type":"reasoning","summary":[]},"unknown":true}`}},
		{"null state", "", []string{`null`}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var state []byte
			for _, payload := range test.states {
				frame := make([]byte, 8+len(payload))
				copy(frame, "OARI")
				binary.BigEndian.PutUint32(frame[4:8], uint32(len(payload)))
				copy(frame[8:], payload)
				state = append(state, frame...)
			}
			request := &chat.Request{Messages: []chat.Message{
				chat.NewUserMessage(chat.NewTextPart("question")),
				chat.NewAssistantMessage(chat.NewReasoningPart(test.text, state)),
			}}
			if _, err := model.Call(t.Context(), request); err == nil {
				t.Fatal("invalid reasoning state reached native replay")
			}
			if _, err := model.CountInputTokens(t.Context(), request); err == nil {
				t.Fatal("invalid reasoning state reached token counting")
			}
		})
	}
	if hits.Load() != 0 {
		t.Fatalf("invalid state reached provider %d time(s)", hits.Load())
	}
}
