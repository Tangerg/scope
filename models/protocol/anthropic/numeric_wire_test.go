package anthropic_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/protocol/anthropic"
)

type numericTransport struct {
	body  []byte
	calls int
}

func (n *numericTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	n.calls++
	var err error
	n.body, err = io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"captured","type":"invalid_request_error"}}`)), Request: request}, nil
}

func TestToolSchemaAndRestoredArgumentsKeepExactNumbers(t *testing.T) {
	boundary := &numericTransport{}
	model, err := anthropic.NewMessages(t.Context(), anthropic.MessagesConfig{APIKey: "test", HTTPClient: &http.Client{Transport: boundary}, BaseURL: "https://provider.example.test", DefaultOptions: chat.Options{Model: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	messages := []chat.Message{
		chat.NewUserMessage(chat.NewTextPart("lookup")),
		chat.NewAssistantMessage(chat.NewToolCallPart(chat.ToolCall{ID: "call", Name: "lookup", Arguments: `{"id":9007199254740993,"ratio":12345678901234567890.0123456789}`})),
		chat.NewToolMessage(chat.ToolResult{ID: "call", Name: "lookup", Output: chat.NewTextToolOutput("found")}),
	}
	stored, err := jsonv2.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	var restored []chat.Message
	if decodeErr := jsonv2.Unmarshal(stored, &restored); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	request, err := chat.NewRequest(restored...)
	if err != nil {
		t.Fatal(err)
	}
	const schema = `{"type":"object","properties":{"id":{"const":9007199254740993}},"required":["id"]}`
	request.Tools = []chat.ToolDefinition{{Name: "lookup", InputSchema: json.RawMessage(schema)}}
	format, err := chat.NewJSONSchemaOutputFormat(chat.JSONSchemaConfig{Name: "answer", Schema: json.RawMessage(schema)})
	if err != nil {
		t.Fatal(err)
	}
	request.Options.OutputFormat = &format
	if setErr := request.Options.Extensions.Set(anthropic.RequestExtensionKey, map[string]any{"future": map[string]any{"number": json.Number("9007199254740993")}}); setErr != nil {
		t.Fatal(setErr)
	}
	_, err = model.Call(t.Context(), request)
	if err == nil || boundary.calls != 1 {
		t.Fatalf("error=%v calls=%d", err, boundary.calls)
	}
	body := string(boundary.body)
	for _, value := range []string{`"id":9007199254740993`, `"ratio":12345678901234567890.0123456789`, `"number":9007199254740993`} {
		if !strings.Contains(body, value) {
			t.Fatalf("missing exact %s: %s", value, body)
		}
	}
	if strings.Contains(body, "9007199254740992") || strings.Count(body, `"const":9007199254740993`) != 2 {
		t.Fatalf("lossy schema: %s", body)
	}
	boundary.calls = 0
	_, countErr := model.CountInputTokens(t.Context(), request)
	if countErr == nil || boundary.calls != 1 {
		t.Fatalf("count error=%v calls=%d", countErr, boundary.calls)
	}
	if strings.Count(string(boundary.body), `"const":9007199254740993`) != 2 || !strings.Contains(string(boundary.body), `"id":9007199254740993`) {
		t.Fatalf("lossy count request: %s", boundary.body)
	}

}
