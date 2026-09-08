package openai_test

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3/responses"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/modeltest"
)

func TestResponsesTerminalStatesMatchAcrossCallAndStream(t *testing.T) {
	tests := []struct {
		name, status, detail, output string
		want                         chat.FinishReason
		wantError                    string
	}{
		{name: "completed", status: "completed", want: chat.FinishReasonStop},
		{name: "length", status: "incomplete", detail: `"incomplete_details":{"reason":"max_output_tokens"},`, want: chat.FinishReasonLength},
		{name: "filtered", status: "incomplete", detail: `"incomplete_details":{"reason":"content_filter"},`, want: chat.FinishReasonContentFilter},
		{name: "unknown incomplete reason", status: "incomplete", detail: `"incomplete_details":{"reason":"unknown"},`, want: chat.FinishReasonOther},
		{name: "partial tool call", status: "incomplete", detail: `"incomplete_details":{"reason":"max_output_tokens"},`, output: `{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{"}`, want: chat.FinishReasonLength},
		{name: "refusal and tool call", status: "completed", output: `{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{}"},{"type":"message","role":"assistant","content":[{"type":"refusal","refusal":"declined"}]}`, want: chat.FinishReasonRefusal},
		{name: "failed", status: "failed", detail: `"error":{"code":"server_error","message":"generation failed"},`, wantError: "openai responses: failed: server_error: generation failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"id":"resp_terminal","model":"gpt-5","created_at":1700000000,"status":%q,%s"output":[%s],"usage":{"input_tokens":7,"output_tokens":3}}`, test.status, test.detail, test.output)
			request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("hello")))
			if err != nil {
				t.Fatal(err)
			}
			server := modeltest.JSONServer(http.StatusOK, body, func(*http.Request) {})
			t.Cleanup(server.Close)
			response, callErr := newResponsesModel(t, server.URL, "gpt-5").Call(t.Context(), request)
			event := "response." + test.status
			streamServer := modeltest.AnthropicSSEServer([]modeltest.AnthropicEvent{{Event: event, Data: fmt.Sprintf(`{"type":%q,"response":%s}`, event, body)}})
			t.Cleanup(streamServer.Close)
			var terminal *chat.ResponseDelta
			var streamErr error
			for delta, err := range newResponsesModel(t, streamServer.URL, "gpt-5").Stream(t.Context(), request) {
				if err != nil {
					streamErr = err
					break
				}
				terminal = delta
			}
			if test.wantError != "" {
				if callErr == nil || callErr.Error() != test.wantError || streamErr == nil || streamErr.Error() != test.wantError {
					t.Fatalf("Call error = %v, Stream error = %v; want %q", callErr, streamErr, test.wantError)
				}
				if response != nil || terminal != nil {
					t.Fatal("failed response exposed a successful result")
				}
				return
			}
			if callErr != nil || streamErr != nil {
				t.Fatalf("Call error = %v, Stream error = %v", callErr, streamErr)
			}
			if terminal == nil {
				t.Fatal("stream omitted terminal response")
			}
			if response.Output.FinishReason != test.want || terminal.FinishReason != test.want {
				t.Fatalf("Call finish = %q, Stream finish = %q; want %q", response.Output.FinishReason, terminal.FinishReason, test.want)
			}
			for _, metadata := range []*chat.ResponseMetadata{response.Metadata, terminal.Metadata} {
				if metadata.ID != "resp_terminal" || metadata.Model != "gpt-5" || metadata.CreatedAt.Unix() != 1700000000 || metadata.Usage.InputTokens != 7 || metadata.Usage.OutputTokens != 3 {
					t.Fatalf("terminal identity = %q/%q at %v, usage = %+v", metadata.ID, metadata.Model, metadata.CreatedAt, metadata.Usage)
				}
			}
		})
	}
}

func TestResponsesCallRejectsNonSuccessfulStates(t *testing.T) {
	for _, status := range []string{"queued", "in_progress", "", "unknown", string(responses.ResponseStatusCancelled)} {
		t.Run(status, func(t *testing.T) {
			server := modeltest.JSONServer(http.StatusOK, fmt.Sprintf(`{"status":%q,"output":[]}`, status), func(*http.Request) {})
			t.Cleanup(server.Close)
			request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("hello")))
			if err != nil {
				t.Fatal(err)
			}
			response, err := newResponsesModel(t, server.URL, "gpt-5").Call(t.Context(), request)
			if err == nil || response != nil {
				t.Fatalf("Call = %v, %v; want failure", response, err)
			}
			if status != string(responses.ResponseStatusCancelled) && !errors.Is(err, chat.ErrInvalidResponse) {
				t.Fatalf("error = %v, want ErrInvalidResponse", err)
			}
		})
	}
}

func TestResponsesStreamRejectsErrorAndPrematureEOF(t *testing.T) {
	for _, test := range []struct {
		name      string
		events    []modeltest.AnthropicEvent
		wantError string
	}{
		{name: "empty EOF", wantError: "stream ended without a terminal response"},
		{name: "partial EOF", events: []modeltest.AnthropicEvent{{Event: "response.output_text.delta", Data: `{"type":"response.output_text.delta","delta":"partial"}`}}, wantError: "stream ended without a terminal response"},
		{name: "error event", events: []modeltest.AnthropicEvent{{Event: "error", Data: `{"type":"error","code":"server_error","message":"generation failed"}`}}, wantError: "server_error: generation failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := modeltest.AnthropicSSEServer(test.events)
			t.Cleanup(server.Close)
			request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("hello")))
			if err != nil {
				t.Fatal(err)
			}
			var got error
			for _, err := range newResponsesModel(t, server.URL, "gpt-5").Stream(t.Context(), request) {
				if err != nil {
					got = err
				}
			}
			if got == nil || !strings.Contains(got.Error(), test.wantError) {
				t.Fatalf("error = %v, want %q", got, test.wantError)
			}
			if test.name != "error event" && !errors.Is(got, chat.ErrInvalidResponse) {
				t.Fatalf("error = %v, want ErrInvalidResponse", got)
			}
		})
	}
}
