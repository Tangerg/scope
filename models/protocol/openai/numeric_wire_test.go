package openai_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/protocol/openai"
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

func TestOpenJSONNumbersRemainExactOnTheWire(t *testing.T) {
	const schema = `{"type":"object","properties":{"id":{"const":9007199254740993}},"required":["id"]}`
	for _, defaults := range []bool{false, true} {
		t.Run(map[bool]string{false: "request", true: "defaults"}[defaults], func(t *testing.T) {
			boundary := &numericTransport{}
			options := chat.Options{Model: "test"}
			if err := options.Extensions.Set(openai.RequestExtensionKey, map[string]any{"seed": int64(9007199254740993), "future": map[string]any{"number": json.Number("12345678901234567890.0123456789")}}); err != nil {
				t.Fatal(err)
			}
			config := openai.ChatCompletionsConfig{APIKey: "test", HTTPClient: &http.Client{Transport: boundary}, BaseURL: "https://provider.example.test"}
			if defaults {
				config.DefaultOptions = options
			}
			model, err := openai.NewChatCompletions(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("request")))
			if err != nil {
				t.Fatal(err)
			}
			if !defaults {
				request.Options = options
			}
			request.Tools = []chat.ToolDefinition{{Name: "identify", InputSchema: json.RawMessage(schema)}}
			format, err := chat.NewJSONSchemaOutputFormat(chat.JSONSchemaConfig{Name: "answer", Schema: json.RawMessage(schema)})
			if err != nil {
				t.Fatal(err)
			}
			request.Options.OutputFormat = &format
			_, err = model.Call(t.Context(), request)
			if err == nil || boundary.calls != 1 {
				t.Fatalf("error=%v calls=%d", err, boundary.calls)
			}
			var wire struct {
				Seed int64 `json:"seed"`
			}
			if err := jsonv2.Unmarshal(boundary.body, &wire); err != nil {
				t.Fatal(err)
			}
			body := string(boundary.body)
			if wire.Seed != 9007199254740993 || !strings.Contains(body, `12345678901234567890.0123456789`) || strings.Contains(body, `9007199254740992`) || strings.Count(body, `"const":9007199254740993`) != 2 {
				t.Fatalf("lossy request: %s", body)
			}
		})
	}
}

func TestResponsesGenerationAndCountingKeepExactSchemas(t *testing.T) {
	boundary := new(numericTransport)
	model, err := openai.NewResponses(t.Context(), openai.ResponsesConfig{APIKey: "test", HTTPClient: &http.Client{Transport: boundary}, BaseURL: "https://provider.example.test", DefaultOptions: chat.Options{Model: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("request")))
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
	for _, count := range []bool{false, true} {
		boundary.calls = 0
		if count {
			_, err = model.CountInputTokens(t.Context(), request)
		} else {
			_, err = model.Call(t.Context(), request)
		}
		if err == nil || boundary.calls != 1 {
			t.Fatalf("count=%t calls=%d error=%v", count, boundary.calls, err)
		}
		if strings.Count(string(boundary.body), `"const":9007199254740993`) != 2 {
			t.Fatalf("count=%t lossy schemas: %s", count, boundary.body)
		}
	}
}
