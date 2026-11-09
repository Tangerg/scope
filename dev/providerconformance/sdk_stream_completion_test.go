package providerconformance_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/models/anthropic"
	"github.com/Tangerg/scope/models/openai"
)

func TestSDKChatCompletionRequiresSuccessfulClose(t *testing.T) {
	const anthropicEvents = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"answer\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	for _, sample := range []struct {
		name     string
		wire     string
		newModel func(context.Context, *http.Client) (chat.Model, error)
	}{
		{name: "chat completions", wire: "data: {\"id\":\"m\",\"model\":\"gpt\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", newModel: func(ctx context.Context, client *http.Client) (chat.Model, error) {
			return openai.NewChatCompletions(ctx, openai.ChatCompletionsConfig{APIKey: "test", BaseURL: responseBudgetEndpoint, HTTPClient: client, DefaultOptions: chat.Options{Model: "gpt"}})
		}},
		{name: "responses", wire: "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"item_id\":\"m\",\"delta\":\"answer\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"model\":\"gpt\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"id\":\"m\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"answer\",\"annotations\":[]}]}]}}\n\n", newModel: func(ctx context.Context, client *http.Client) (chat.Model, error) {
			return openai.NewResponses(ctx, openai.ResponsesConfig{APIKey: "test", BaseURL: responseBudgetEndpoint, HTTPClient: client, DefaultOptions: chat.Options{Model: "gpt"}})
		}},
		{name: "messages", wire: anthropicEvents, newModel: func(ctx context.Context, client *http.Client) (chat.Model, error) {
			return anthropic.NewMessages(ctx, anthropic.MessagesConfig{APIKey: "test", BaseURL: responseBudgetEndpoint, HTTPClient: client, DefaultOptions: chat.Options{Model: "claude"}})
		}},
	} {
		t.Run(sample.name, func(t *testing.T) {
			fault := errors.New("stream close failed")
			for _, closeErr := range []error{nil, fault} {
				transport := &responseBudgetTransport{status: http.StatusOK, contentType: "text/event-stream", closeErr: closeErr, reader: func(*http.Request) io.Reader { return strings.NewReader(sample.wire) }}
				model, err := sample.newModel(t.Context(), &http.Client{Transport: transport})
				if err != nil {
					t.Fatal(err)
				}
				request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}}
				response, err := model.Call(t.Context(), request)
				if closeErr == nil {
					if err != nil || response == nil {
						t.Fatalf("response=%v error=%v", response, err)
					}
				} else if response != nil || !errors.Is(err, fault) || invocation.ClassifyFailure(err) != invocation.FailureUnknown {
					t.Fatalf("response=%v error=%v", response, err)
				}
				failures, finished := 0, 0
				for delta, err := range model.(chat.Streamer).Stream(t.Context(), request) {
					if err != nil {
						if !errors.Is(err, closeErr) {
							t.Fatal(err)
						}
						failures++
					}
					if delta != nil && delta.FinishReason != "" {
						finished++
					}
				}
				if closeErr != nil && (failures != 1 || finished != 0) || closeErr == nil && (failures != 0 || finished != 1) {
					t.Fatalf("close=%v failures=%d finished=%d", closeErr, failures, finished)
				}
				if len(transport.bodies) != 2 || transport.bodies[0].closed != 1 || transport.bodies[1].closed != 1 {
					t.Fatalf("bodies=%+v", transport.bodies)
				}
			}
		})
	}
}
