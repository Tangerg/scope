package openai_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/protocol/openai"
)

func TestCompletionBindsItsUsageAndToolsToTheGeneratingResponse(t *testing.T) {
	const start = `{"id":"answer","model":"gpt","created":7,"choices":[{"index":0,"delta":{"content":"prefix"}}]}`
	const finish = `{"id":"answer","model":"gpt","created":7,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
	for _, tc := range []struct {
		name, tail string
		want       error
	}{
		{"wrong billing identity", `{"id":"other","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":3}}`, chat.ErrInvalidResponse},
		{"wrong billing model", `{"model":"other","choices":[]}`, chat.ErrInvalidResponse},
		{"wrong creation time", `{"created":8,"choices":[]}`, chat.ErrInvalidResponse},
		{"no additional identity", `{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":3}}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := modeltest.OpenAISSEServer([]string{start, finish, tc.tail})
			defer server.Close()
			model, err := openai.NewChatCompletions(t.Context(), openai.ChatCompletionsConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: chat.Options{Model: "gpt"}}, testChatDialect())
			if err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(t.Context(), newToolStreamRequest(t))
			if tc.want != nil {
				if response != nil || !errors.Is(err, tc.want) {
					t.Fatalf("mixed answer = %v, %v", response, err)
				}
				return
			}
			if err != nil || response.Metadata.ID != "answer" || response.Metadata.Usage.OutputTokens != 3 || response.Text() != "prefix" {
				t.Fatalf("consistent tail = %+v, %v", response, err)
			}
		})
	}
	for _, index := range []string{"", `"index":null,`, `"index":-1,`, `"index":"0",`, `"index":0.5,`} {
		model := newToolStreamModel(t, []string{fmt.Sprintf(`{%s"id":"call","type":"function","function":{"name":"run","arguments":"{}"}}`, index)})
		if res, err := chat.Collect(model.Stream(t.Context(), newToolStreamRequest(t))); res != nil || !errors.Is(err, chat.ErrInvalidResponse) {
			t.Fatalf("ambiguous index %s produced %v, %v", index, res, err)
		}
	}
}

func TestCompletionRequiresReportedChoiceIndex(t *testing.T) {
	for _, sample := range []struct {
		name  string
		index string
	}{
		{"missing", ""},
		{"null", `"index":null,`},
		{"negative", `"index":-1,`},
		{"different choice", `"index":1,`},
		{"quoted", `"index":"0",`},
		{"fractional", `"index":0.5,`},
		{"boolean", `"index":false,`},
	} {
		t.Run(sample.name, func(t *testing.T) {
			model := newLifecycleStreamModel(t, []string{
				fmt.Sprintf(`{%s"delta":{"content":"answer"},"finish_reason":"stop"}`, sample.index),
			})
			request := newToolStreamRequest(t)
			if response, err := model.Call(t.Context(), request); response != nil || !errors.Is(err, chat.ErrInvalidResponse) {
				t.Fatalf("Call = %#v, %v; want ErrInvalidResponse", response, err)
			}
			var streamErr error
			for delta, err := range model.Stream(t.Context(), request) {
				if err != nil {
					streamErr = err
					break
				}
				if len(delta.Parts) != 0 || delta.FinishReason != "" {
					t.Fatalf("unidentified choice emitted output: %#v", delta)
				}
			}
			if !errors.Is(streamErr, chat.ErrInvalidResponse) {
				t.Fatalf("Stream error = %v; want ErrInvalidResponse", streamErr)
			}
		})
	}
}

func TestDeprecatedFunctionCallCannotEscapeTypedToolAdmission(t *testing.T) {
	for _, name := range []string{"functions", "function_call"} {
		transport := new(numericTransport)
		model, err := openai.NewChatCompletions(t.Context(), openai.ChatCompletionsConfig{APIKey: "test", BaseURL: "https://api.example", HTTPClient: &http.Client{Transport: transport}, DefaultOptions: chat.Options{Model: "m"}}, testChatDialect())
		if err != nil {
			t.Fatal(err)
		}
		req := newToolStreamRequest(t)
		if err = req.Options.Extensions.Set(openai.RequestExtensionKey, map[string]any{name: "auto"}); err != nil {
			t.Fatal(err)
		}
		if res, callErr := model.Call(t.Context(), req); res != nil || callErr == nil || transport.calls != 0 || invocation.ClassifyFailure(callErr) != invocation.FailureNotExecuted {
			t.Fatalf("%s: %v, %v, %d attempts", name, res, callErr, transport.calls)
		}
	}
	for choice, want := range map[string]error{
		`{"index":0,"delta":{"function_call":{"name":"legacy","arguments":"{}"}},"finish_reason":"stop"}`: errors.ErrUnsupported,
		`{"index":0,"delta":{},"finish_reason":"function_call"}`:                                          errors.ErrUnsupported,
		`{"index":0,"delta":{},"finish_reason":"tool_calls"}`:                                             chat.ErrInvalidResponse,
	} {
		model := newLifecycleStreamModel(t, []string{choice})
		if res, err := model.Call(t.Context(), newToolStreamRequest(t)); res != nil || !errors.Is(err, want) {
			t.Fatalf("invalid tool outcome %v, %v", res, err)
		}
	}
}

func TestResponsesTerminalPreservesPreviouslyReportedIdentityWithoutContent(t *testing.T) {
	server := modeltest.AnthropicSSEServer([]modeltest.AnthropicEvent{
		{Event: "response.created", Data: `{"type":"response.created","response":{"id":"receipt","model":"reported","created_at":3,"status":"in_progress"}}`},
		{Event: "response.completed", Data: `{"type":"response.completed","response":{"status":"completed","output":[]}}`},
	})
	defer server.Close()
	model := newResponsesModel(t, server.URL, "asked-for")
	res, err := model.Call(t.Context(), newToolStreamRequest(t))
	if err != nil || res == nil || res.Metadata == nil || res.Metadata.ID != "receipt" || res.Metadata.Model != "reported" || res.Metadata.CreatedAt.Unix() != 3 {
		t.Fatalf("final identity = %+v, %v", res, err)
	}
}

func TestResponsesRejectsCoercedPositionsAndNeverCitesAnotherItem(t *testing.T) {
	first := modeltest.AnthropicEvent{Event: "response.output_text.delta", Data: `{"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"a","delta":"first"}`}
	second := modeltest.AnthropicEvent{Event: "response.output_text.delta", Data: `{"type":"response.output_text.delta","output_index":1,"content_index":0,"item_id":"b","delta":"second"}`}
	annotation := func(output, content, item string) modeltest.AnthropicEvent {
		return modeltest.AnthropicEvent{Event: "response.output_text.annotation.added", Data: fmt.Sprintf(`{"type":"response.output_text.annotation.added","output_index":%s,"content_index":%s,"item_id":%q,"annotation_index":0,"annotation":{"type":"url_citation","url":"https://e.example","title":"Evidence","start_index":0,"end_index":1}}`, output, content, item)}
	}
	for _, item := range []struct {
		name string
		tail modeltest.AnthropicEvent
		want error
	}{
		{"earlier text", annotation("0", "0", "a"), errors.ErrUnsupported},
		{"wrong item", annotation("1", "0", "a"), chat.ErrInvalidResponse},
		{"unknown segment", annotation("1", "1", "b"), chat.ErrInvalidResponse},
		{"rounded output", annotation("1.5", "0", "b"), chat.ErrInvalidResponse},
		{"quoted content", annotation("1", `"0"`, "b"), chat.ErrInvalidResponse},
		{"tool index from boolean", modeltest.AnthropicEvent{Event: "response.output_item.added", Data: `{"type":"response.output_item.added","output_index":false,"item":{"type":"function_call","call_id":"tool","id":"fc","name":"lookup","arguments":""}}`}, chat.ErrInvalidResponse},
	} {
		t.Run(item.name, func(t *testing.T) {
			server := modeltest.AnthropicSSEServer([]modeltest.AnthropicEvent{first, second, item.tail})
			defer server.Close()
			model := newResponsesModel(t, server.URL, "model")
			var got error
			for delta, err := range model.Stream(t.Context(), newResponsesChatRequest(t)) {
				if err != nil {
					got = err
					break
				}
				for _, part := range delta.Parts {
					if part.Kind == chat.PartDeltaCitation || part.Kind == chat.PartDeltaToolCall {
						t.Fatalf("emitted evidence under a different identity: %+v", part)
					}
				}
			}
			if !errors.Is(got, item.want) {
				t.Fatalf("expected %v, got %v", item.want, got)
			}
		})
	}
}
