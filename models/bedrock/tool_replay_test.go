package bedrock

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	corechat "github.com/Tangerg/scope/core/chat"
)

func TestEmptyToolObjectsReachBedrockWire(t *testing.T) {
	var captured struct {
		AdditionalModelRequestFields json.RawMessage `json:"additionalModelRequestFields"`
		Messages                     []struct {
			Content []struct {
				ToolUse *struct {
					Input json.RawMessage `json:"input"`
				} `json:"toolUse"`
				ToolResult *struct {
					Content []struct {
						JSON json.RawMessage `json:"json"`
					} `json:"content"`
				} `json:"toolResult"`
			} `json:"content"`
		} `json:"messages"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := jsonv2.UnmarshalRead(request.Body, &captured); err != nil {
			t.Errorf("request: %v", err)
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
	}))
	t.Cleanup(server.Close)
	model := &Chat{api: &api{}, defaults: corechat.Options{Model: "anthropic.claude-test"}}
	input, _, err := model.buildConverseStreamInput(&corechat.Request{Messages: []corechat.Message{
		corechat.NewUserMessage(corechat.NewTextPart("read the clock")),
		corechat.NewAssistantMessage(corechat.NewToolCallPart(corechat.ToolCall{ID: "call-1", Name: "clock", Arguments: `{}`})),
		corechat.NewToolMessage(corechat.ToolResult{ID: "call-1", Name: "clock", Output: corechat.ToolOutput{Details: json.RawMessage(`{}`)}}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	client := bedrockruntime.NewFromConfig(aws.Config{
		Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test-key", "test-secret", ""),
		HTTPClient: server.Client(), RetryMaxAttempts: 1,
	}, func(options *bedrockruntime.Options) {
		options.BaseEndpoint = aws.String(server.URL)
	})
	output, err := client.ConverseStream(t.Context(), input)
	if err != nil {
		t.Fatalf("SDK rejected no-argument tool continuation: %v", err)
	}
	t.Cleanup(func() { _ = output.GetStream().Close() })
	if len(captured.Messages) != 3 || len(captured.Messages[1].Content) != 1 || len(captured.Messages[2].Content) != 1 {
		t.Fatalf("request messages = %#v", captured.Messages)
	}
	if captured.AdditionalModelRequestFields != nil {
		t.Fatalf("unset additional fields reached wire: %s", captured.AdditionalModelRequestFields)
	}
	call := captured.Messages[1].Content[0].ToolUse
	if call == nil || string(call.Input) != `{}` {
		t.Fatalf("tool input = %#v, want {}", call)
	}
	result := captured.Messages[2].Content[0].ToolResult
	if result == nil || len(result.Content) != 1 || string(result.Content[0].JSON) != `{}` {
		t.Fatalf("tool result = %#v, want {}", result)
	}
}

func TestToolJSONDocumentsPreserveMeaningfulEmptyValues(t *testing.T) {
	for _, raw := range []string{`{}`, `[]`, `null`, `0`, `false`} {
		t.Run(raw, func(t *testing.T) {
			blocks, err := mapToolResultContent(corechat.ToolOutput{Details: json.RawMessage(raw)})
			if err != nil {
				t.Fatal(err)
			}
			if len(blocks) != 1 {
				t.Fatalf("tool result blocks = %#v", blocks)
			}
			block := blocks[0].(*types.ToolResultContentBlockMemberJson)
			if block.Value == nil {
				t.Fatalf("explicit %s became an absent JSON document", raw)
			}
			encoded, err := block.Value.MarshalSmithyDocument()
			if err != nil || string(encoded) != raw {
				t.Fatalf("JSON document = %s, %v; want %s", encoded, err, raw)
			}
		})
	}
	block, include, err := mapProtocolPart(corechat.NewToolCallPart(corechat.ToolCall{ID: "call-empty", Name: "clock"}))
	if err != nil || !include {
		t.Fatalf("no-argument tool = %#v, %t, %v", block, include, err)
	}
	encoded, err := block.(*types.ContentBlockMemberToolUse).Value.Input.MarshalSmithyDocument()
	if err != nil || string(encoded) != `{}` {
		t.Fatalf("absent argument text = %s, %v; want {}", encoded, err)
	}
}
