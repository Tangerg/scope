package openai_test

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
)

func TestResponsesReplaysOriginalAssistantPhase(t *testing.T) {
	type inputItem struct {
		Role    string `json:"role"`
		Phase   string `json:"phase"`
		Content any    `json:"content"`
	}
	var input []inputItem
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var captured struct {
			Input []inputItem `json:"input"`
		}
		if err := jsonv2.UnmarshalRead(request.Body, &captured); err != nil {
			t.Error(err)
			return
		}
		input = captured.Input
		if strings.HasSuffix(request.URL.Path, "/input_tokens") {
			writer.Header().Set("Content-Type", "application/json")
			fmt.Fprint(writer, `{"input_tokens":12}`)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		first := `{"type":"message","id":"commentary-1","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"Checking."}]}`
		last := `{"type":"message","id":"answer-1","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Done."}]}`
		for _, event := range []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"commentary-1","role":"assistant","phase":"commentary","content":[]}}`,
			`{"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"commentary-1","delta":"Checking."}`,
			`{"type":"response.output_item.done","output_index":0,"item":` + first + `}`,
			`{"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"answer-1","role":"assistant","phase":"final_answer","content":[]}}`,
			`{"type":"response.output_text.delta","output_index":1,"content_index":0,"item_id":"answer-1","delta":"Done."}`,
			`{"type":"response.output_item.done","output_index":1,"item":` + last + `}`,
			`{"type":"response.completed","response":{"id":"resp-1","model":"model","status":"completed","output":[` + first + `,` + last + `]}}`,
		} {
			fmt.Fprintf(writer, "data: %s\n\n", event)
		}
	}))
	t.Cleanup(server.Close)
	model := newResponsesModel(t, server.URL, "model")
	request := newResponsesChatRequest(t)
	response, err := model.Call(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Output.Message.Parts) != 2 {
		t.Fatalf("message phases merged into %d part(s)", len(response.Output.Message.Parts))
	}
	message := response.Output.Message.Clone()
	message.Parts[0].Text = "Edited checking."
	wire, err := jsonv2.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if unmarshalErr := jsonv2.Unmarshal(wire, &message); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	request.Messages = append(request.Messages, message, chat.NewUserMessage(chat.NewTextPart("continue")))
	for _, count := range []bool{false, true} {
		if count {
			_, err = model.CountInputTokens(t.Context(), request)
		} else {
			_, err = model.Call(t.Context(), request)
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(input) != 4 || input[1].Phase != "commentary" || input[1].Content != "Edited checking." || input[2].Phase != "final_answer" || input[2].Content != "Done." || input[3].Phase != "" {
			t.Fatalf("replayed input = %#v", input)
		}
	}
}
