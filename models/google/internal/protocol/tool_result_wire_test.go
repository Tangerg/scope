package protocol_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

func TestChatToolResultEnvelopeHasOneStatusOwner(t *testing.T) {
	const payload = `{"error":"business error field","output":"business output field","id":9007199254740993}`
	for _, provider := range []string{"google", "vertexai"} {
		for _, structured := range []bool{false, true} {
			for _, isError := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/structured=%t/error=%t", provider, structured, isError), func(t *testing.T) {
					captured := make(chan map[string]json.RawMessage, 1)
					model := newNativeContractChat(t, provider, func(writer http.ResponseWriter, request *http.Request) {
						var body struct {
							Contents []struct {
								Parts []struct {
									FunctionResponse struct {
										Response map[string]json.RawMessage `json:"response"`
									} `json:"functionResponse"`
								} `json:"parts"`
							} `json:"contents"`
						}
						if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
							t.Error(err)
							http.Error(writer, "invalid request", http.StatusBadRequest)
							return
						}
						if len(body.Contents) != 3 || len(body.Contents[2].Parts) != 1 {
							t.Errorf("tool history = %#v", body.Contents)
							http.Error(writer, "invalid tool history", http.StatusBadRequest)
							return
						}
						captured <- body.Contents[2].Parts[0].FunctionResponse.Response
						nativeChatChunks(`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"done"}]},"finishReason":"STOP"}]}`)(writer, request)
					})
					output := toolcontract.NewTextOutput(payload)
					if structured {
						output = toolcontract.Output{Details: json.RawMessage(payload)}
					}
					request := &corechat.Request{Messages: []corechat.Message{
						corechat.NewUserMessage(corechat.NewTextPart("inspect")),
						corechat.NewAssistantMessage(corechat.NewToolCallPart(corechat.ToolCall{ID: "call-1", Name: "inspect", Arguments: `{}`})),
						corechat.NewToolMessage(corechat.ToolResult{ID: "call-1", Name: "inspect", Output: output, IsError: isError}),
					}}
					if _, err := model.Call(t.Context(), request); err != nil {
						t.Fatal(err)
					}
					response := <-captured
					key := "output"
					if isError {
						key = "error"
					}
					if len(response) != 1 || len(response[key]) == 0 {
						t.Fatalf("native response = %#v; want only the %q envelope", response, key)
					}
					var data map[string]json.RawMessage
					if err := jsonv2.Unmarshal(response[key], &data); err != nil {
						t.Fatal(err)
					}
					if len(data) != 3 || string(data["error"]) != `"business error field"` || string(data["output"]) != `"business output field"` || string(data["id"]) != "9007199254740993" {
						t.Fatalf("business data = %s", response[key])
					}
				})
			}
		}
	}
}
