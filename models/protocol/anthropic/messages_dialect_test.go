package anthropic

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"

	corechat "github.com/Tangerg/scope/core/chat"
)

func TestDialectPreparesResolvedRequestWithoutMutatingCaller(t *testing.T) {
	request := &corechat.Request{
		Messages:   []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))},
		Tools:      []corechat.ToolDefinition{{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		ToolChoice: &corechat.ToolChoice{Mode: corechat.ToolChoiceAuto},
	}
	dialect := Dialect{Provider: "compatible", PrepareRequest: func(resolved *corechat.Request, fields map[string]any) error {
		if resolved.Options.ReasoningEffort != "high" || resolved.ToolChoice.Mode != corechat.ToolChoiceAuto {
			t.Fatalf("request policy did not receive effective options and tool choice: %#v", resolved)
		}
		resolved.Options.ReasoningEffort = ""
		resolved.ToolChoice.Mode = corechat.ToolChoiceNone
		resolved.Messages[0].Parts[0].Text = "prepared"
		fields["service_tier"] = "auto"
		return nil
	}}
	params, err := mapProtocolRequest(corechat.Options{Model: "compatible-model", ReasoningEffort: "high"}, request, dialect)
	if err != nil {
		t.Fatal(err)
	}
	if params.OutputConfig.Effort != "" || params.ToolChoice.OfNone == nil || params.Messages[0].Content[0].OfText.Text != "prepared" {
		t.Fatalf("prepared request = %#v", params)
	}
	if request.Options.Model != "" || request.ToolChoice.Mode != corechat.ToolChoiceAuto || request.Messages[0].Parts[0].Text != "hello" {
		t.Fatalf("policy mutated caller request: %#v", request)
	}
}

func TestConfiguredHeadersCanRemoveSDKDefault(t *testing.T) {
	for _, removeDefault := range []bool{false, true} {
		name := "native"
		if removeDefault {
			name = "compatible"
		}
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				_, exists := request.Header["X-Api-Key"]
				if exists == removeDefault {
					t.Errorf("X-Api-Key present = %t, want %t", exists, !removeDefault)
				}
				if removeDefault && request.Header.Get("api-key") != "test-key" {
					t.Error("configured provider authentication header is missing")
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(`{"input_tokens":1}`))
			}))
			defer server.Close()
			var headers http.Header
			if removeDefault {
				headers = http.Header{"X-Api-Key": nil, "Api-Key": []string{"test-key"}}
			}
			transport, err := newAPI(apiConfig{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client(), Headers: headers})
			if err != nil {
				t.Fatal(err)
			}
			response, err := transport.countTokens(t.Context(), &anthropicsdk.MessageCountTokensParams{
				Model: "test-model", Messages: []anthropicsdk.MessageParam{anthropicsdk.NewUserMessage(anthropicsdk.NewTextBlock("hello"))},
			})
			if err != nil || response.InputTokens != 1 {
				t.Fatalf("CountTokens = %#v, %v", response, err)
			}
		})
	}
}
