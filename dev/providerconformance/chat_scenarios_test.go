package providerconformance_test

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/dev/providerconformance/internal/chatcheck"
	"github.com/Tangerg/scope/models/anthropic"
	"github.com/Tangerg/scope/models/google"
	"github.com/Tangerg/scope/models/openai"
)

func runChatScenario(t *testing.T, entry chatcheck.Entry, scenario, key, modelID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	budget := int64(2)
	if scenario == "tool_round_trip" {
		budget = 4
	}
	client := newLiveHTTPClient(t, entry.BaseURL, budget)
	construct := func(t *testing.T) (chat.Model, chat.Streamer) {
		return newChatBinding(t, ctx, entry, client, key, modelID)
	}
	if scenario == "tool_round_trip" {
		runChatToolRoundTrip(t, ctx, construct)
		return
	}
	assertion := func(t *testing.T, response *chat.Response) {
		if response.Output.FinishReason != chat.FinishReasonStop || response.Metadata == nil || response.Metadata.Usage == nil {
			t.Fatal("expected complete response with reported usage")
		}
		if scenario == "json_schema" {
			var value struct {
				Answer string `json:"answer"`
			}
			if decodeErr := jsonv2.Unmarshal([]byte(response.Text()), &value, jsonv2.RejectUnknownMembers(true)); decodeErr != nil || value.Answer != "scope" {
				t.Fatal("response did not satisfy the requested JSON schema and answer")
			}
		} else if strings.TrimSpace(response.Text()) != "SCOPE_OK" {
			t.Fatal("response did not preserve the requested text answer")
		}
	}
	modeltest.RunChatContract(t, modeltest.ChatContract{
		New:        construct,
		Request:    func(t *testing.T) *chat.Request { return chatScenarioRequest(t, scenario) },
		AssertCall: assertion, AssertAggregated: assertion,
	})
}

func newChatBinding(t *testing.T, ctx context.Context, entry chatcheck.Entry, client *http.Client, key, modelID string) (chat.Model, chat.Streamer) {
	t.Helper()
	var err error
	var model chat.Model
	var streamer chat.Streamer
	switch entry.ID {
	case "openai-chat":
		adapter, constructErr := openai.NewChatCompletions(ctx, openai.ChatCompletionsConfig{APIKey: key, BaseURL: entry.BaseURL, HTTPClient: client, DefaultOptions: chat.Options{Model: modelID}})
		err, model, streamer = constructErr, adapter, adapter
	case "openai-responses":
		adapter, constructErr := openai.NewResponses(ctx, openai.ResponsesConfig{APIKey: key, BaseURL: entry.BaseURL, HTTPClient: client, DefaultOptions: chat.Options{Model: modelID}})
		err, model, streamer = constructErr, adapter, adapter
	case "anthropic-messages":
		adapter, constructErr := anthropic.NewMessages(ctx, anthropic.MessagesConfig{APIKey: key, BaseURL: entry.BaseURL, HTTPClient: client, DefaultOptions: chat.Options{Model: modelID}})
		err, model, streamer = constructErr, adapter, adapter
	case "google-generate-content":
		adapter, constructErr := google.NewChat(ctx, google.ChatConfig{APIKey: key, BaseURL: entry.BaseURL, HTTPClient: client, DefaultOptions: chat.Options{Model: modelID}})
		err, model, streamer = constructErr, adapter, adapter
	default:
		t.Fatal("live binding has no constructor")
	}
	if err != nil {
		t.Fatalf("construct live binding: %T", err)
	}
	return model, streamer
}

func chatScenarioRequest(t *testing.T, scenario string) *chat.Request {
	t.Helper()
	prompt := "Reply with exactly SCOPE_OK and no other text."
	if scenario == "json_schema" {
		prompt = "Return JSON with answer equal to scope."
	}
	request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart(prompt)))
	if err != nil {
		t.Fatal(err)
	}
	limit := int64(256)
	request.Options.MaxOutputTokens = &limit
	if scenario == "json_schema" {
		format, formatErr := chat.NewJSONSchemaOutputFormat(chat.JSONSchemaConfig{Name: "answer", Schema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`)})
		if formatErr != nil {
			t.Fatal(formatErr)
		}
		request.Options.OutputFormat = &format
	}
	return request
}

func runChatToolRoundTrip(t *testing.T, ctx context.Context, construct func(*testing.T) (chat.Model, chat.Streamer)) {
	t.Helper()
	for _, mode := range []string{"call", "stream"} {
		t.Run(mode, func(t *testing.T) {
			model, streamer := construct(t)
			request := chatScenarioRequest(t, "text")
			request.Messages = []chat.Message{chat.NewUserMessage(chat.NewTextPart("Use scope_probe with nonce scope_nonce_719. Then reply with only the token returned by the tool."))}
			request.Tools = []chat.ToolDefinition{{Name: "scope_probe", Description: "Return a local test token", InputSchema: json.RawMessage(`{"type":"object","properties":{"nonce":{"type":"string"}},"required":["nonce"],"additionalProperties":false}`)}}
			request.ToolChoice = &chat.ToolChoice{Mode: chat.ToolChoiceNamed, Name: "scope_probe"}
			invoke := func(request *chat.Request) *chat.Response {
				if mode == "call" {
					response, err := model.Call(ctx, request)
					if err != nil {
						t.Fatalf("Call: %T", err)
					}
					return response
				}
				var accumulator chat.ResponseAccumulator
				for delta, streamErr := range streamer.Stream(ctx, request) {
					if streamErr != nil {
						t.Fatalf("Stream: %T", streamErr)
					}
					if addErr := accumulator.Add(delta); addErr != nil {
						t.Fatal(addErr)
					}
				}
				response, responseErr := accumulator.Response()
				if responseErr != nil {
					t.Fatal(responseErr)
				}
				return response
			}
			response := invoke(request)
			if response == nil || response.Validate() != nil || response.Output.FinishReason != chat.FinishReasonToolCalls || response.Output.Message == nil {
				t.Fatal("expected a complete tool proposal")
			}
			var calls []chat.ToolCall
			for _, part := range response.Output.Message.Parts {
				if part.ToolCall != nil {
					calls = append(calls, *part.ToolCall)
				}
			}
			if len(calls) != 1 || calls[0].Name != "scope_probe" {
				t.Fatal("expected exactly the named local tool")
			}
			var arguments struct {
				Nonce string `json:"nonce"`
			}
			if err := jsonv2.Unmarshal([]byte(calls[0].Arguments), &arguments, jsonv2.RejectUnknownMembers(true)); err != nil || arguments.Nonce != "scope_nonce_719" {
				t.Fatal("tool arguments changed")
			}
			request.Messages = append(request.Messages, response.Output.Message.Clone(), chat.NewToolMessage(chat.ToolResult{ID: calls[0].ID, Name: calls[0].Name, Output: chat.NewTextToolOutput("SCOPE_REPLAY_831")}))
			request.ToolChoice = &chat.ToolChoice{Mode: chat.ToolChoiceNone}
			final := invoke(request)
			if final == nil || final.Validate() != nil || final.Output.FinishReason != chat.FinishReasonStop || strings.TrimSpace(final.Text()) != "SCOPE_REPLAY_831" {
				t.Fatal("tool result did not survive the next provider request")
			}
		})
	}
}
